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

	replies, err := os.ReadFile(filepath.Join(root, "replies.txt"))
	if err != nil {
		t.Fatalf("replies: %v\n%s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(string(replies)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 replies, got %d: %s", len(lines), replies)
	}
	if !strings.Contains(lines[0], `"ok":false`) || !strings.Contains(lines[0], "outside") {
		t.Fatalf("outside-path config was not refused: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"ok":true`) {
		t.Fatalf("valid start was refused: %s", lines[1])
	}
	if !strings.Contains(lines[2], `"ok":true`) {
		t.Fatalf("stop was refused: %s", lines[2])
	}
	if !strings.Contains(stdout.String(), "started demo") {
		t.Fatalf("supervisor did not report the start: %s", stdout.String())
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
	script := fmt.Sprintf(`#!/bin/sh
echo '{"v":1,"type":"hello","version":"test"}'
printf '{"v":1,"type":"request","id":"1","want":"start","cluster":"demo","members":[{"name":"member-1","config":%%s}]}\n' '%s'
read -r one
printf '{"v":1,"type":"request","id":"2","want":"start","cluster":"demo","members":[{"name":"member-1","config":%%s}]}\n' '%s'
read -r two
echo '{"v":1,"type":"request","id":"3","want":"stop","cluster":"demo"}'
read -r three
printf '%%s\n%%s\n%%s\n' "$one" "$two" "$three" > "%s/replies.txt"
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
