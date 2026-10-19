package agents

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

const sample = `{
  // my theme — hand-edited, must survive
  "theme": "Gruvbox Light",
  "agent_servers": {
    "oc": {
      "args": ["acp"],
      "command": "/Users/dev/.opencode/bin/opencode",
      "type": "custom"
    }
  },
  "context_server_timeout": 80
}
`

func writeSample(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zed", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustDoc(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	doc, err := decodeDoc(raw)
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	return doc
}

func TestUpsertAddsTheAgentAndKeepsEverythingElse(t *testing.T) {
	path := writeSample(t)
	e := Entry{Name: "workspace", Command: "/tmp/workspace-agent", Args: []string{"-driver", "tmpdir"}}

	res, err := Upsert(path, e, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Backup == "" {
		t.Fatalf("changed=%v backup=%q", res.Changed, res.Backup)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "// my theme") {
		t.Errorf("the comment was lost:\n%s", got)
	}
	doc := mustDoc(t, got)
	if doc["theme"] != "Gruvbox Light" || doc["context_server_timeout"] != float64(80) {
		t.Errorf("unrelated keys changed: %v", doc)
	}
	servers := doc["agent_servers"].(map[string]any)
	if _, ok := servers["oc"]; !ok {
		t.Errorf("the oc entry was lost: %v", servers)
	}
	if servers["workspace"].(map[string]any)["command"] != "/tmp/workspace-agent" {
		t.Errorf("workspace entry = %v", servers["workspace"])
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	path := writeSample(t)
	e := Entry{Name: "workspace", Command: "/tmp/workspace-agent", Args: []string{"-driver", "tmpdir"}}

	first, err := Upsert(path, e, false)
	if err != nil || !first.Changed {
		t.Fatalf("first: changed=%v err=%v", first.Changed, err)
	}
	afterFirst, _ := os.ReadFile(path)

	second, err := Upsert(path, e, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed {
		t.Error("second upsert reported a change")
	}
	if second.Backup != "" {
		t.Errorf("second upsert made a backup: %s", second.Backup)
	}
	afterSecond, _ := os.ReadFile(path)
	if !bytes.Equal(afterFirst, afterSecond) {
		t.Error("idempotent upsert changed the file")
	}
}

func TestUpsertCreatesTheFileAndTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	e := Entry{Name: "workspace", Command: "/tmp/workspace-agent"}

	res, err := Upsert(path, e, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Backup != "" {
		t.Errorf("backup for a file that did not exist: %s", res.Backup)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := mustDoc(t, got)
	servers := doc["agent_servers"].(map[string]any)
	if _, ok := servers["workspace"]; !ok {
		t.Fatalf("entry missing:\n%s", got)
	}
}

func TestUpsertReplacesAChangedEntry(t *testing.T) {
	path := writeSample(t)
	if _, err := Upsert(path, Entry{Name: "workspace", Command: "/old"}, false); err != nil {
		t.Fatal(err)
	}
	res, err := Upsert(path, Entry{Name: "workspace", Command: "/new", Args: []string{"-session", "7"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("replacement was not reported")
	}
	got, _ := os.ReadFile(path)
	doc := mustDoc(t, got)
	servers := doc["agent_servers"].(map[string]any)
	if servers["workspace"].(map[string]any)["command"] != "/new" {
		t.Errorf("entry = %v", servers["workspace"])
	}
	if _, ok := servers["oc"]; !ok {
		t.Errorf("oc was lost: %v", servers)
	}
}

func TestRemoveKeepsTheSiblings(t *testing.T) {
	path := writeSample(t)
	if _, err := Upsert(path, Entry{Name: "workspace", Command: "/tmp/workspace-agent"}, false); err != nil {
		t.Fatal(err)
	}
	res, err := Remove(path, "workspace", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("remove reported no change")
	}
	got, _ := os.ReadFile(path)
	doc := mustDoc(t, got)
	servers := doc["agent_servers"].(map[string]any)
	if _, ok := servers["workspace"]; ok {
		t.Errorf("workspace still present:\n%s", got)
	}
	if _, ok := servers["oc"]; !ok {
		t.Errorf("oc was removed too:\n%s", got)
	}
	if !strings.Contains(string(got), "// my theme") {
		t.Errorf("comment lost:\n%s", got)
	}
}

func TestRemoveLastEntryDropsAgentServers(t *testing.T) {
	path := writeSample(t)
	if _, err := Remove(path, "oc", false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	doc := mustDoc(t, got)
	if _, ok := doc["agent_servers"]; ok {
		t.Errorf("agent_servers survived with no entries:\n%s", got)
	}
}

func TestRemoveMissingIsNoop(t *testing.T) {
	path := writeSample(t)
	before, _ := os.ReadFile(path)
	res, err := Remove(path, "nobody", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || res.Backup != "" {
		t.Errorf("changed=%v backup=%q", res.Changed, res.Backup)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("noop remove changed the file")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	path := writeSample(t)
	before, _ := os.ReadFile(path)
	res, err := Upsert(path, Entry{Name: "workspace", Command: "/x"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.After == nil {
		t.Fatalf("dry run: changed=%v", res.Changed)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("dry run wrote the file")
	}
	if res.Backup != "" {
		t.Errorf("dry run made a backup: %s", res.Backup)
	}
}

func TestInvalidSettingsAreRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	broken := []byte(`{"theme": "x",`)
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Upsert(path, Entry{Name: "workspace", Command: "/x"}, false); err == nil {
		t.Fatal("invalid settings were accepted")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, broken) {
		t.Error("the refused file was modified")
	}
}

func TestEntryIsIndentedLikeItsSiblings(t *testing.T) {
	path := writeSample(t)
	if _, err := Upsert(path, Entry{
		Name:    "workspace",
		Command: "/tmp/workspace-agent",
		Args:    []string{"-driver", "tmpdir"},
	}, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := `    "workspace": {
      "args": ["-driver", "tmpdir"],
      "command": "/tmp/workspace-agent",
      "type": "custom"
    }`
	if !strings.Contains(string(got), want) {
		t.Errorf("entry is not formatted like its siblings.\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestZedSettingsPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got, err := ZedSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/xdg/zed/settings.json" {
		t.Errorf("path = %q", got)
	}
}

func TestResultRoundTripsThroughHujson(t *testing.T) {
	path := writeSample(t)
	res, err := Upsert(path, Entry{Name: "workspace", Command: "/x"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hujson.Parse(res.After); err != nil {
		t.Fatalf("dry-run result is not JWCC: %v\n%s", err, res.After)
	}
}
