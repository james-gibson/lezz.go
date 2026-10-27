//go:build darwin

package sea

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSupervisorStartsStopsAndRefusesOutsidePaths drives the channel with a
// fake app: hello, a start whose derived config points outside the lab root
// (refused), a valid start, and a stop. The real code paths are the reader,
// binder, config writer, argv build and process-group stop; only the two
// executables are fakes.
func TestSupervisorStartsStopsAndRefusesOutsidePaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".lezz", "sea", "Clusters")
	valid := configText(filepath.Join(root, "demo", "member-1"))
	outside := configText(filepath.Join(t.TempDir(), "not-ours"))

	app := makeFakeApp(t, root, valid, outside)
	tool := makeFakeTool(t)
	appLog := filepath.Join(t.TempDir(), "app.log")
	stdout := &bytes.Buffer{}
	var stderr bytes.Buffer

	err := Run(context.Background(), RunOptions{
		AppPath:      app,
		ClustersRoot: root,
		LogPath:      appLog,
		Out:          stdout,
		Err:          &stderr,
		FindTool:     func(string) (string, error) { return tool, nil },
	})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr.String())
	}

	transcript, err := os.ReadFile(filepath.Join(root, "lines.txt"))
	if err != nil {
		t.Fatalf("channel transcript: %v\n%s", err, stderr.String())
	}
	line := strings.Split(strings.TrimSpace(string(transcript)), "\n")
	if len(line) != 4 {
		t.Fatalf("expected hub + 3 replies, got %d: %s", len(line), transcript)
	}
	replies := line[1:]
	if !strings.Contains(replies[0], `"ok":false`) || !strings.Contains(replies[0], "outside") {
		t.Fatalf("outside-path config was not refused: %s", replies[0])
	}
	if !strings.Contains(replies[1], `"ok":true`) {
		t.Fatalf("valid start was refused: %s", replies[1])
	}
	if !strings.Contains(replies[2], `"ok":true`) {
		t.Fatalf("stop was refused: %s", replies[2])
	}
	if !strings.Contains(stdout.String(), "started demo") {
		t.Fatalf("supervisor did not report the start: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "session hub:") {
		t.Fatalf("supervisor did not report the hub: %s", stdout.String())
	}

	// The hub is announced once, before the first reply, and it is loopback.
	if !strings.Contains(line[0], `"type":"hub"`) || !strings.Contains(line[0], "127.0.0.1:") {
		t.Fatalf("hub announcement is not a loopback hub: %s", line[0])
	}
	hubConfig, err := os.ReadFile(filepath.Join(root, "hub", "adhd.yaml"))
	if err != nil {
		t.Fatalf("hub config missing: %v", err)
	}
	if !strings.Contains(string(hubConfig), `addr: "127.0.0.1:`) {
		t.Fatalf("hub config is not loopback: %s", hubConfig)
	}
	if strings.Contains(string(hubConfig), "0.0.0.0") {
		t.Fatalf("hub config binds every interface: %s", hubConfig)
	}

	// The supervisor owns the instance directory and wrote the derived config.
	configPath := filepath.Join(root, "demo", "member-1", "config.yaml")
	if _, statErr := os.Stat(configPath); statErr != nil {
		t.Fatalf("derived config missing: %v", statErr)
	}
	if _, err := readPID(filepath.Join(root, "demo", "member-1", "ocd-smoke-alarm.pid")); !os.IsNotExist(err) {
		t.Fatalf("alarm.pid was not removed by stop: %v", err)
	}
}

func configText(dir string) string {
	return "runtime:\n" +
		"  state_dir: \"" + filepath.Join(dir, "state") + "\"\n" +
		"  lock_file: \"" + filepath.Join(dir, "ocd-smoke-alarm.pid") + "\"\n" +
		"  baseline_file: \"" + filepath.Join(dir, "state", "known-good.json") + "\"\n"
}

func makeFakeApp(t *testing.T, root, valid, outside string) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), "lezz.sea.app")
	macos := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o750); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>co.jamesgibson.LezzSea</string>
<key>CFBundleExecutable</key><string>fake-sea</string>
</dict></plist>
`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}

	badBody, _ := json.Marshal(outside)
	goodBody, _ := json.Marshal(valid)
	// Four lines on the wire, in a fixed order: the supervisor answers the
	// hello with the hub announcement before it can answer the first request,
	// so the app sees hub, reply, reply, reply. The app writes them all; the
	// test splits them.
	script := fmt.Sprintf(`#!/bin/sh
echo '{"v":1,"type":"hello","version":"test"}'
printf '{"v":1,"type":"request","id":"1","want":"start","cluster":"demo","members":[{"name":"member-1","config":%%s}]}\n' '%s'
read -r one
printf '{"v":1,"type":"request","id":"2","want":"start","cluster":"demo","members":[{"name":"member-1","config":%%s}]}\n' '%s'
read -r two
echo '{"v":1,"type":"request","id":"3","want":"stop","cluster":"demo"}'
read -r three
read -r four
printf '%%s\n%%s\n%%s\n%%s\n' "$one" "$two" "$three" "$four" > "%s/lines.txt"
`, string(badBody), string(goodBody), root)
	path := filepath.Join(macos, "fake-sea")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	return app
}

func makeFakeTool(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "ops" ]; then
  exit 0
fi
cfg=""
for a in "$@"; do
  case "$a" in
    --config=*) cfg="${a#--config=}" ;;
  esac
done
if [ -n "$cfg" ]; then
  echo $$ > "$(dirname "$cfg")/ocd-smoke-alarm.pid"
fi
exec sleep 30
`
	path := filepath.Join(t.TempDir(), "ocd-smoke-alarm")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	return path
}

// TestHubConfigBindsLoopbackOnly pins the exposure decision: the hub is a
// control surface, so its address is loopback and its channel light is a single
// named entry.
func TestHubConfigBindsLoopbackOnly(t *testing.T) {
	withChannel := hubConfig(9090, "http://127.0.0.1:19999/status")
	if !strings.Contains(withChannel, `addr: "127.0.0.1:9090"`) {
		t.Fatalf("hub does not bind loopback: %s", withChannel)
	}
	if !strings.Contains(withChannel, `name: "lezz.sea"`) {
		t.Fatalf("hub does not watch the channel light: %s", withChannel)
	}
	if !strings.Contains(withChannel, `endpoint: "http://127.0.0.1:19999/status"`) {
		t.Fatalf("hub channel endpoint is wrong: %s", withChannel)
	}

	empty := hubConfig(9090, "")
	if strings.Contains(empty, "smoke_alarm") {
		t.Fatalf("an empty hub invented a light: %s", empty)
	}
}

// TestPortAllocatorNeverHandsOutTheSamePortTwice is the regression pin for the
// listen-close race between the hub and a cluster adhd.
func TestPortAllocatorNeverHandsOutTheSamePortTwice(t *testing.T) {
	ports := newPortAllocator()
	first, err := ports.acquire(0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	second, err := ports.acquire(0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if first == second {
		t.Fatalf("allocator handed out %d twice", first)
	}
	if first < clusterPortLow || first > clusterPortHigh {
		t.Fatalf("port %d is outside the cluster range", first)
	}
}

// TestStopOrderPutsTheHubLast holds the topology's stop order: every cluster
// first, the hub last, on both exit paths.
func TestStopOrderPutsTheHubLast(t *testing.T) {
	s := &supervisor{children: map[string]*child{
		"/root/x/member-1/ocd-smoke-alarm.pid": {pidPath: "/root/x/member-1/ocd-smoke-alarm.pid", kind: "alarm"},
		"/root/y/adhd.pid":                     {pidPath: "/root/y/adhd.pid", kind: "adhd"},
		"/root/hub/adhd.pid":                   {pidPath: "/root/hub/adhd.pid", kind: "hub"},
	}}
	order := s.stopOrder()
	if len(order) != 3 {
		t.Fatalf("expected 3 children, got %v", order)
	}
	if order[len(order)-1] != "/root/hub/adhd.pid" {
		t.Fatalf("the hub is not stopped last: %v", order)
	}
}

// TestStopRefusesTheHub pins that a stop request cannot take the session hub
// down: it outlives every cluster.
func TestStopRefusesTheHub(t *testing.T) {
	s := &supervisor{children: map[string]*child{}, ports: newPortAllocator()}
	s.binder = &Binder{Root: t.TempDir()}
	if err := s.stop(Message{Cluster: HubSlug}); err == nil {
		t.Fatal("stop accepted the hub")
	}
}
