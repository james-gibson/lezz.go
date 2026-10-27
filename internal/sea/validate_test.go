package sea

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinderConfinesPaths(t *testing.T) {
	root := t.TempDir()
	binder, err := NewBinder(root)
	if err != nil {
		t.Fatal(err)
	}

	inside := filepath.Join(root, "demo", "config.yaml")
	if _, err := binder.Path(inside, ".yaml"); err != nil {
		t.Fatalf("inside path rejected: %v", err)
	}
	if _, err := binder.Path(filepath.Join(root, "..", "escape.yaml"), ".yaml"); err == nil {
		t.Fatal("path outside the root was accepted")
	}
	if _, err := binder.Path(filepath.Join(root, "demo", "config.yml"), ".yaml"); err == nil {
		t.Fatal("wrong extension was accepted")
	}
}

func TestBinderValidatesRuntimePaths(t *testing.T) {
	root := t.TempDir()
	binder, err := NewBinder(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "demo", "member-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	good := filepath.Join(dir, "config.yaml")
	writeConfig(t, good, dir)
	if _, err := binder.Config(good); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	bad := filepath.Join(dir, "bad.yaml")
	writeConfig(t, bad, "/tmp/not-ours")
	if _, err := binder.Config(bad); err == nil {
		t.Fatal("config writing outside the root was accepted")
	}

	relative := filepath.Join(dir, "relative.yaml")
	writeConfig(t, relative, "state")
	if _, err := binder.Config(relative); err == nil {
		t.Fatal("relative runtime path was accepted")
	}

	missing := filepath.Join(dir, "missing.yaml")
	if err := os.WriteFile(missing, []byte("runtime:\n  state_dir: \""+filepath.Join(dir, "state")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := binder.Config(missing); err == nil {
		t.Fatal("config without lock_file/baseline_file was accepted")
	}
}

func TestDecodeRequest(t *testing.T) {
	line := `{"v":1,"type":"request","id":"7","want":"adhd","slug":"demo","healthPorts":[18088],"gherkinDirectory":"/checkout/features"}`
	msg, err := Decode([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Want != WantADHD || msg.Slug != "demo" || len(msg.HealthPorts) != 1 {
		t.Fatalf("decoded %+v", msg)
	}
	if msg.GherkinDirectory != "/checkout/features" {
		t.Fatalf("gherkin dir = %q", msg.GherkinDirectory)
	}
}

func TestReplyCarriesTheVerdict(t *testing.T) {
	ok := ReplyOK("1")
	raw, err := json.Marshal(ok)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("ok reply = %s", raw)
	}

	bad := ReplyError("2", "nope")
	raw, err = json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"error":"nope"`) {
		t.Fatalf("error reply = %s", raw)
	}
}

func writeConfig(t *testing.T, path, stateDir string) {
	t.Helper()
	body := "runtime:\n" +
		"  state_dir: \"" + stateDir + "\"\n" +
		"  lock_file: \"" + filepath.Join(stateDir, "alarm.pid") + "\"\n" +
		"  baseline_file: \"" + filepath.Join(stateDir, "known-good.json") + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
