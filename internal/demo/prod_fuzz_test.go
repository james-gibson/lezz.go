package demo

import (
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The prod command has one deliberately pure core: ProdOptions, the topology
// plan, the argv the rotator installs, and the three config templates. Those
// are fuzzed here. Nothing that starts a process, binds a port, writes a plist
// or loads launchd is reachable from a fuzz target on purpose.
//
//	go test ./internal/demo -run=^$ -fuzz=FuzzBuildProdPlan -fuzztime=60s

func FuzzProdOptionsNormalized(f *testing.F) {
	for _, seed := range []string{
		"prod", "lab-42", "", "Prod", "-x", "a b", "9", strings.Repeat("x", 70),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, isotope string) {
		out, err := ProdOptions{Isotope: isotope}.normalized()
		if err != nil {
			return
		}
		if !prodIsotopePattern.MatchString(out.Isotope) {
			t.Fatalf("accepted an isotope outside the grammar: %q", out.Isotope)
		}
		if out.Isotope == "" || out.BasePort == 0 || out.RotateTimeout <= 0 {
			t.Fatalf("normalized options left a zero value: %+v", out)
		}
		again, err := out.normalized()
		if err != nil {
			t.Fatalf("already-normalized options rejected: %v", err)
		}
		if again != out {
			t.Fatalf("normalization is not idempotent: %+v -> %+v", out, again)
		}
	})
}

func FuzzProdPortLayout(f *testing.F) {
	for _, seed := range []int{19200, 1024, maxProdBasePort, 0, -1, 65535, 65000} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, base int) {
		opts, err := ProdOptions{BasePort: base}.normalized()
		if err != nil {
			return
		}

		seen := map[int]bool{}
		for _, p := range prodPorts(opts.BasePort) {
			if p < 1 || p > 65535 {
				t.Fatalf("port %d out of range for base %d", p, opts.BasePort)
			}
			if seen[p] {
				t.Fatalf("port %d appears twice for base %d", p, opts.BasePort)
			}
			seen[p] = true
		}
		if len(seen) != 9 {
			t.Fatalf("base %d produced %d distinct ports, want 9: %v", opts.BasePort, len(seen), seen)
		}
		if !seen[ProdRegistryPort] {
			t.Fatalf("base %d dropped the anchor port %d", opts.BasePort, ProdRegistryPort)
		}

		nodes := prodNodes(opts.BasePort)
		for i, n := range nodes {
			if n.AlarmPort != opts.BasePort+i {
				t.Fatalf("node %d alarm port = %d, want %d", i, n.AlarmPort, opts.BasePort+i)
			}
			if n.ADHDPort != opts.BasePort+prodADHDPortOffset+i {
				t.Fatalf("node %d adhd port = %d", i, n.ADHDPort)
			}
		}
		if !seen[opts.BasePort+prodFederationOffset] || !seen[opts.BasePort+prodFederationOffset+1] {
			t.Fatalf("base %d lost a federation slot: %v", opts.BasePort, seen)
		}
	})
}

func FuzzBuildProdPlan(f *testing.F) {
	for _, seed := range []struct {
		isotope string
		base    int
		host    string
	}{
		{"lab-42", 19200, "127.0.0.1"},
		{"prod", 1024, "192.168.1.5"},
		{"9", maxProdBasePort, ""},
		{"a-b-c", 0, "host name"},
		{"Prod", 19200, "::1"},
		{"", -1, ""},
	} {
		f.Add(seed.isotope, seed.base, seed.host)
	}
	f.Fuzz(func(t *testing.T, isotope string, base int, host string) {
		plan, err := buildProdPlan(ProdOptions{Isotope: isotope, BasePort: base}, host)
		if err != nil {
			return
		}
		opts := plan.Options

		if len(plan.Nodes) != 3 {
			t.Fatalf("plan has %d nodes", len(plan.Nodes))
		}
		if plan.Primary.Role != "a" || plan.Fallback.Role != "b" {
			t.Fatalf("roles = %q/%q", plan.Primary.Role, plan.Fallback.Role)
		}
		if !plan.Primary.Federate || !plan.Nodes[1].Federate {
			t.Fatal("the A pair must federate")
		}
		if plan.Fallback.Federate {
			t.Fatal("the fallback must not federate with the A pair")
		}

		if plan.Cluster.Name != opts.Isotope {
			t.Fatalf("cluster name = %q, want %q", plan.Cluster.Name, opts.Isotope)
		}
		if plan.Cluster.AlarmA == plan.Cluster.AlarmB {
			t.Fatalf("alarm_a and alarm_b are the same endpoint: %q", plan.Cluster.AlarmA)
		}

		// The advertised contract: four fields, no third alarm.
		payload, err := json.Marshal(plan.Cluster)
		if err != nil {
			t.Fatalf("marshal cluster: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatalf("unmarshal cluster: %v", err)
		}
		if len(fields) != 4 {
			t.Fatalf("cluster advertises %d fields, want 4: %s", len(fields), payload)
		}
		for _, forbidden := range []string{"alarm_c", "alarm-c"} {
			if _, ok := fields[forbidden]; ok {
				t.Fatalf("cluster leaks %q: %s", forbidden, payload)
			}
		}
		if params := string(clusterJoinParams(plan.Cluster)); strings.Contains(params, "alarm_c") {
			t.Fatalf("join payload leaks a third alarm: %s", params)
		}

		// Isotope identity: the logical names only.
		if len(plan.Isotopes) != 3 {
			t.Fatalf("got %d isotope records", len(plan.Isotopes))
		}
		want := map[string]string{
			opts.Isotope:        "cluster",
			opts.Isotope + "-a": "alarm-a",
			opts.Isotope + "-b": "alarm-b",
		}
		for _, rec := range plan.Isotopes {
			role, ok := want[rec.Name]
			if !ok || rec.Role != role {
				t.Fatalf("unexpected isotope record %q role %q", rec.Name, rec.Role)
			}
		}

		if plan.FederationBasePort != opts.BasePort+prodFederationOffset {
			t.Fatalf("federation base = %d", plan.FederationBasePort)
		}
	})
}

func FuzzParseProdArgs(f *testing.F) {
	for _, seed := range []string{
		"--isotope\x00lab-42",
		"--isotope=lab-42\x00--base-port=19200",
		"--rotate",
		"--child\x00--generation\x002",
		"--generation\x00-1",
		"--base-port\x0065535",
		"positional",
		"--isotope\x00PROD",
		"--unknown",
		"",
		"--isotope=\x00--base-port=0",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		args := strings.Split(raw, "\x00")
		opts, err := ParseProdArgs(args, io.Discard)
		if err != nil {
			return
		}
		// Accepting an argument set means the command must be able to run it.
		if _, err := opts.normalized(); err != nil {
			t.Fatalf("parser accepted options normalization rejects: %v (%+v)", err, opts)
		}
		if !prodIsotopePattern.MatchString(opts.Isotope) {
			t.Fatalf("accepted isotope outside the grammar: %q", opts.Isotope)
		}
		if opts.BasePort < 1024 || opts.BasePort > maxProdBasePort {
			t.Fatalf("accepted base port out of range: %d", opts.BasePort)
		}
	})
}

func FuzzProdChildRoundTrip(f *testing.F) {
	for _, seed := range []struct {
		isotope string
		base    int
		gen     int
	}{
		{"lab-42", 19200, 0},
		{"prod", 1024, 7},
		{"9", maxProdBasePort, 1},
		{"Prod", 1, -1},
		{"", 0, 0},
	} {
		f.Add(seed.isotope, seed.base, seed.gen)
	}
	f.Fuzz(func(t *testing.T, isotope string, base, generation int) {
		opts, err := (ProdOptions{Isotope: isotope, BasePort: base, Generation: generation}).normalized()
		if err != nil || opts.Generation == math.MaxInt {
			return
		}
		next := opts.Generation + 1

		// A rotated child is the same identity at the same endpoints: the argv
		// installed for it must parse back to exactly that, with only the
		// generation advanced and --child set.
		args := prodServiceArgs(opts, next)
		if len(args) == 0 || args[0] != "prod" {
			t.Fatalf("service argv does not start with the command: %v", args)
		}
		got, err := ParseProdArgs(args[1:], io.Discard)
		if err != nil {
			t.Fatalf("child argv does not parse: %v (%v)", err, args[1:])
		}
		if got.Isotope != opts.Isotope {
			t.Fatalf("isotope changed across rotation: %q -> %q", opts.Isotope, got.Isotope)
		}
		if got.BasePort != opts.BasePort {
			t.Fatalf("base port changed across rotation: %d -> %d", opts.BasePort, got.BasePort)
		}
		if got.Generation != next || !got.Child || got.Rotate {
			t.Fatalf("child flags wrong: %+v (want gen %d, child, no rotate)", got, next)
		}
	})
}

func FuzzProdTemplates(f *testing.F) {
	for _, seed := range []struct {
		isotope string
		base    int
	}{
		{"lab-42", 19200},
		{"prod", 1024},
		{"9", maxProdBasePort},
		{"Prod", 1},
	} {
		f.Add(seed.isotope, seed.base)
	}
	f.Fuzz(func(t *testing.T, isotope string, base int) {
		plan, err := buildProdPlan(ProdOptions{Isotope: isotope, BasePort: base}, "127.0.0.1")
		if err != nil {
			return
		}

		for _, n := range plan.Nodes {
			out, err := renderTemplate(prodAlarmConfigTmpl, plan.alarmConfig(n, "/bin/ocd-smoke-alarm", "/tmp/prod"))
			if err != nil {
				t.Fatalf("render alarm %s: %v", n.Name, err)
			}
			if strings.Contains(out, "{{") {
				t.Fatalf("unrendered template action for %s:\n%s", n.Name, out)
			}

			var doc struct {
				Service struct {
					Environment string `yaml:"environment"`
				} `yaml:"service"`
				Targets     []any `yaml:"targets"`
				RemoteAgent struct {
					ManagedUpdates bool `yaml:"managed_updates"`
				} `yaml:"remote_agent"`
				Federation *struct {
					Enabled   bool   `yaml:"enabled"`
					ClusterID string `yaml:"cluster_id"`
				} `yaml:"federation"`
			}
			if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("alarm %s is not valid YAML: %v\n%s", n.Name, err, out)
			}
			if doc.Service.Environment != "lezz-prod" {
				t.Fatalf("alarm %s environment = %q", n.Name, doc.Service.Environment)
			}
			if len(doc.Targets) != 0 {
				t.Fatalf("alarm %s hardcodes %d targets; prod targets arrive at runtime", n.Name, len(doc.Targets))
			}
			if !doc.RemoteAgent.ManagedUpdates {
				t.Fatalf("alarm %s does not accept remote management", n.Name)
			}
			if gotFed := doc.Federation != nil && doc.Federation.Enabled; gotFed != n.Federate {
				t.Fatalf("alarm %s federates=%v, want %v", n.Name, gotFed, n.Federate)
			}
			if n.Federate && doc.Federation.ClusterID != plan.Options.Isotope+"-a" {
				t.Fatalf("alarm %s cluster_id = %q", n.Name, doc.Federation.ClusterID)
			}

			cfgOut, err := renderTemplate(prodADHDConfigTmpl, plan.adhdConfig(n))
			if err != nil {
				t.Fatalf("render adhd %s: %v", n.Name, err)
			}
			var client struct {
				MCP struct {
					Addr string `yaml:"addr"`
				} `yaml:"mcp_server"`
				SmokeAlarm []struct {
					Name     string `yaml:"name"`
					Endpoint string `yaml:"endpoint"`
				} `yaml:"smoke_alarm"`
			}
			if err := yaml.Unmarshal([]byte(cfgOut), &client); err != nil {
				t.Fatalf("adhd %s config is not valid YAML: %v\n%s", n.Name, err, cfgOut)
			}
			if len(client.SmokeAlarm) != 2 {
				t.Fatalf("adhd %s has %d alarm entries, want 2", n.Name, len(client.SmokeAlarm))
			}
			if client.SmokeAlarm[0].Name != "alarm_a" || client.SmokeAlarm[1].Name != "alarm_b" {
				t.Fatalf("adhd %s arms against %q then %q", n.Name, client.SmokeAlarm[0].Name, client.SmokeAlarm[1].Name)
			}
			if !strings.Contains(client.SmokeAlarm[0].Endpoint, strconv.Itoa(plan.Primary.AlarmPort)) {
				t.Fatalf("alarm_a endpoint = %q", client.SmokeAlarm[0].Endpoint)
			}
			if !strings.Contains(client.SmokeAlarm[1].Endpoint, strconv.Itoa(plan.Fallback.AlarmPort)) {
				t.Fatalf("alarm_b endpoint = %q", client.SmokeAlarm[1].Endpoint)
			}
			if strings.Contains(cfgOut, "alarm_c") || strings.Contains(cfgOut, "alarm-c") {
				t.Fatalf("adhd %s config names the fallback node physically:\n%s", n.Name, cfgOut)
			}
		}
	})
}
