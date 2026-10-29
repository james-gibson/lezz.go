package demo

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"text/template"
	"time"
)

func TestProdOptionsNormalized(t *testing.T) {
	got, err := ProdOptions{}.normalized()
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if got.Isotope != "prod" {
		t.Errorf("isotope = %q, want prod", got.Isotope)
	}
	if got.BasePort != ProdDefaultBasePort {
		t.Errorf("base port = %d, want %d", got.BasePort, ProdDefaultBasePort)
	}
	if got.RotateTimeout == 0 {
		t.Error("rotate timeout should default")
	}

	for _, bad := range []ProdOptions{
		{Isotope: "Prod"},
		{Isotope: "-prod"},
		{Isotope: "prod/cluster"},
		{Isotope: strings.Repeat("x", 64)},
		{BasePort: 1023},
		{BasePort: 65524},
		{BasePort: 19100}, // the anchor itself
		{BasePort: 19000}, // federation slot base+100 == 19100
		{BasePort: 19088}, // adhd port base+12 == 19100
		{Generation: -1},
	} {
		if _, err := bad.normalized(); err == nil {
			t.Errorf("%+v: expected an error", bad)
		}
	}
}

// TestProdTopologyKeepsCInvisible is the shape of the whole feature: two
// logical alarms in a client's view, three physical nodes behind them.
func TestProdTopologyKeepsCInvisible(t *testing.T) {
	nodes := prodNodes(19200)
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3", len(nodes))
	}

	// A_a and A_b are one logical role, mirrored.
	if nodes[0].Name != "alarm-a-a" || nodes[0].Role != "a" || !nodes[0].Federate {
		t.Errorf("primary = %+v, want alarm-a-a role a federated", nodes[0])
	}
	if nodes[1].Name != "alarm-a-b" || nodes[1].Role != "a" || !nodes[1].Federate {
		t.Errorf("mirror = %+v, want alarm-a-b role a federated", nodes[1])
	}

	// C is the fallback role, and does not federate with the A pair.
	if nodes[2].Name != "alarm-c" || nodes[2].Role != "b" || nodes[2].Federate {
		t.Errorf("fallback = %+v, want alarm-c role b standalone", nodes[2])
	}

	if got := []int{nodes[0].AlarmPort, nodes[1].AlarmPort, nodes[2].AlarmPort}; got[0] != 19200 || got[1] != 19201 || got[2] != 19202 {
		t.Errorf("alarm ports = %v", got)
	}
	if got := []int{nodes[0].ADHDPort, nodes[1].ADHDPort, nodes[2].ADHDPort}; got[0] != 19210 || got[1] != 19211 || got[2] != 19212 {
		t.Errorf("adhd ports = %v", got)
	}

	seen := map[int]bool{}
	for _, p := range prodPorts(19200) {
		if seen[p] {
			t.Errorf("port %d listed twice", p)
		}
		seen[p] = true
	}
	// registry + 3 alarms + 3 adhd + 2 federation slots
	if len(seen) != 9 {
		t.Errorf("got %d distinct ports, want 9: %v", len(seen), seen)
	}
}

// TestProdRegistryAdvertisesTwoAlarms is the contract the dashboard sees:
// alarm_a and alarm_b, and no third name.
func TestProdRegistryAdvertisesTwoAlarms(t *testing.T) {
	nodes := prodNodes(19200)
	primary, fallback := nodes[0], nodes[2]

	info := ClusterInfo{
		Name:    "lab-42",
		AlarmA:  "http://host:19200",
		AlarmB:  "http://host:19202",
		AdhdMCP: "http://host:19210/mcp",
	}
	payload, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, forbidden := range []string{"alarm_c", "alarm-c", "node_c"} {
		if _, ok := fields[forbidden]; ok {
			t.Errorf("registry payload leaks %q: %s", forbidden, payload)
		}
	}
	for _, want := range []string{"name", "alarm_a", "alarm_b", "adhd_mcp"} {
		if _, ok := fields[want]; !ok {
			t.Errorf("registry payload missing %q: %s", want, payload)
		}
	}

	// C is what alarm_b points at; that is the only place it appears.
	if fallback.AlarmPort != 19202 {
		t.Errorf("fallback port = %d", fallback.AlarmPort)
	}
	// The join wire payload uses the same two names.
	params := string(clusterJoinParams(info))
	if strings.Contains(params, "alarm_c") {
		t.Errorf("join payload leaks a third alarm: %s", params)
	}
	if !strings.Contains(params, `"alarm_b":"http://host:19202"`) {
		t.Errorf("join payload does not advertise the fallback as alarm_b: %s", params)
	}
	_ = primary
}

func TestProdADHDConfigHasOnlyTwoLogicalAlarms(t *testing.T) {
	out := mustRender(t, prodADHDConfigTmpl, prodADHDConfig{
		ADHDPort:   19210,
		AlarmAPort: 19200,
		AlarmBPort: 19202,
	})
	if got := strings.Count(out, "- name:"); got != 2 {
		t.Errorf("got %d alarm entries, want exactly 2 (alarm_a, alarm_b):\n%s", got, out)
	}
	for _, want := range []string{
		`addr: ":19210"`,
		`name: "alarm_a"`,
		`endpoint: "http://127.0.0.1:19200"`,
		`name: "alarm_b"`,
		`endpoint: "http://127.0.0.1:19202"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("adhd config missing %q\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"alarm_c", "19201"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("adhd config leaks %q (the mirror must not be visible):\n%s", forbidden, out)
		}
	}
	// Order matters: the dashboard arms against alarm_a first.
	if strings.Index(out, "alarm_a") > strings.Index(out, "alarm_b") {
		t.Errorf("alarm_a must be listed before alarm_b:\n%s", out)
	}
}

func TestProdAlarmConfigMirrorsOnlyTheAPair(t *testing.T) {
	nodes := prodNodes(19200)
	base := prodAlarmConfig{
		Port:               19200,
		ListenAddr:         "0.0.0.0",
		StateDir:           "/tmp/state-a",
		Binary:             "/bin/ocd-smoke-alarm",
		ConfigPath:         "/tmp/alarm-a.yaml",
		LogPath:            "/tmp/alarm-a.log",
		Federate:           nodes[0].Federate,
		ClusterID:          "lab-42-a",
		FederationBasePort: 19300,
		FederationMaxPort:  19301,
	}
	mirrored := mustRender(t, prodAlarmConfigTmpl, base)
	for _, want := range []string{
		`environment: "lezz-prod"`,
		`cluster_id: "lab-42-a"`,
		`base_port: 19300`,
		"targets: []",
		"remote_agent:",
		"managed_updates: true",
	} {
		if !strings.Contains(mirrored, want) {
			t.Errorf("mirrored alarm config missing %q\n%s", want, mirrored)
		}
	}
	// The mirror must not appear as a monitored target: a dashboard reads the
	// target list, and "alarm-a-b" would tell it the primary is duplicated.
	for _, forbidden := range []string{"alarm-a-b", "alarm-c"} {
		if strings.Contains(mirrored, forbidden) {
			t.Errorf("alarm config leaks %q into the client view:\n%s", forbidden, mirrored)
		}
	}

	base.Federate = false
	standalone := mustRender(t, prodAlarmConfigTmpl, base)
	if strings.Contains(standalone, "federation:") {
		t.Errorf("the fallback node must not federate with the A pair:\n%s", standalone)
	}
}

func TestProdIsotopeRecordsAreLogicalOnly(t *testing.T) {
	nodes := prodNodes(19200)
	records := prodIsotopeRecords("lab-42", nodes[0], nodes[2], "host", "127.0.0.1")

	byName := map[string]string{}
	for _, r := range records {
		byName[r.Name] = r.Role
	}
	want := map[string]string{
		"lab-42":   "cluster",
		"lab-42-a": "alarm-a",
		"lab-42-b": "alarm-b",
	}
	for name, role := range want {
		if byName[name] != role {
			t.Errorf("record %q role = %q, want %q", name, byName[name], role)
		}
	}
	for name := range byName {
		if strings.HasSuffix(name, "-c") || strings.Contains(name, "a-a") || strings.Contains(name, "a-b") {
			t.Errorf("physical node name leaked into isotope records: %q", name)
		}
	}
}

func TestProdServiceArgsInheritIdentity(t *testing.T) {
	opts := ProdOptions{Isotope: "lab-42", BasePort: 21000, Generation: 1}
	args := prodServiceArgs(opts, 2)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"prod",
		"--isotope lab-42",
		"--base-port 21000",
		"--child",
		"--generation 2",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "--generation 1") {
		t.Errorf("child must not inherit the parent's generation: %v", args)
	}
	if profile := prodServiceProfile(2); profile != "prod-g2" {
		t.Errorf("profile = %q, want prod-g2", profile)
	}
}

func TestWaitPortsFree(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if err := waitPortsFree(context.Background(), []int{port}, 300*time.Millisecond); err == nil {
		t.Fatal("expected a timeout while the port is held")
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := waitPortsFree(context.Background(), []int{port}, 2*time.Second); err != nil {
		t.Fatalf("port should be free after close: %v", err)
	}
}

func mustRender(t *testing.T, tmpl *template.Template, data any) string {
	t.Helper()
	out, err := renderTemplate(tmpl, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return out
}
