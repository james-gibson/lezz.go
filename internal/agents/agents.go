// Package agents wires ACP agents into an editor's settings.
//
// Zed keeps custom agents in `agent_servers` inside its JWCC settings file
// (`~/.config/zed/settings.json`). The merge here edits that one member and
// leaves the rest of the file byte-for-byte alone: a host that rewrites an
// editor's settings must not reorder keys or drop comments.
package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"time"
)

// Entry is one `agent_servers.<name>` value, in Zed's custom-agent shape.
type Entry struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// Result describes one Upsert or Remove.
type Result struct {
	Path    string
	Changed bool
	Backup  string
	Before  []byte
	After   []byte
}

// Validate rejects an entry that would be written but would not run.
func (e Entry) Validate() error {
	if e.Name == "" {
		return errors.New("agent name is empty")
	}
	if e.Command == "" {
		return fmt.Errorf("agent %q has no command", e.Name)
	}
	return nil
}

// JSON renders the entry as it is stored under agent_servers.<name>, in the
// same style the file uses (scalar arrays stay on one line).
func (e Entry) JSON() ([]byte, error) {
	return []byte(prettyValue(e.value(), "", "  ")), nil
}

// value is the JSON object Zed expects under agent_servers.<name>.
func (e Entry) value() map[string]any {
	v := map[string]any{
		"type":    "custom",
		"command": e.Command,
	}
	if len(e.Args) > 0 {
		v["args"] = e.Args
	}
	if len(e.Env) > 0 {
		v["env"] = e.Env
	}
	return v
}

// ZedSettingsPath returns Zed's settings file. Zed uses ~/.config/zed on macOS
// and on Linux without a custom XDG_CONFIG_HOME; on Windows it uses a different
// location, which this tool does not guess.
func ZedSettingsPath() (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("zed on Windows uses a different settings path; add the agent from the ACP registry or manually")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "zed", "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "zed", "settings.json"), nil
}

// Upsert adds or updates e in path. With dryRun it computes the change and
// writes nothing.
func Upsert(path string, e Entry, dryRun bool) (Result, error) {
	if err := e.Validate(); err != nil {
		return Result{}, err
	}
	before, existed, err := readFile(path)
	if err != nil {
		return Result{}, err
	}

	after, changed, err := mergeEntry(before, e)
	if err != nil {
		return Result{}, err
	}
	res := Result{Path: path, Changed: changed, Before: before, After: after}
	if !changed || dryRun {
		return res, nil
	}
	if verr := verifyUpsert(before, after, e); verr != nil {
		return Result{}, verr
	}
	if existed {
		if res.Backup, err = writeBackup(path, before); err != nil {
			return Result{}, err
		}
	}
	if err := writeFile(path, after); err != nil {
		return Result{}, err
	}
	return res, nil
}

// Remove deletes agent_servers.<name> from path. With dryRun it computes the
// change and writes nothing.
func Remove(path, name string, dryRun bool) (Result, error) {
	if name == "" {
		return Result{}, errors.New("agent name is empty")
	}
	before, existed, err := readFile(path)
	if err != nil {
		return Result{}, err
	}
	if !existed {
		return Result{Path: path, Before: before, After: before}, nil
	}

	after, changed, err := removeEntry(before, name)
	if err != nil {
		return Result{}, err
	}
	res := Result{Path: path, Changed: changed, Before: before, After: after}
	if !changed || dryRun {
		return res, nil
	}
	if verr := verifyRemove(before, after, name); verr != nil {
		return Result{}, verr
	}
	if res.Backup, err = writeBackup(path, before); err != nil {
		return Result{}, err
	}
	if err := writeFile(path, after); err != nil {
		return Result{}, err
	}
	return res, nil
}

// Show reads the entry at path, if present.
func Show(path, name string) (Entry, bool, error) {
	raw, existed, err := readFile(path)
	if err != nil {
		return Entry{}, false, err
	}
	if !existed {
		return Entry{}, false, nil
	}
	doc, err := decodeDoc(raw)
	if err != nil {
		return Entry{}, false, err
	}
	servers, _ := doc["agent_servers"].(map[string]any)
	obj, ok := servers[name].(map[string]any)
	if !ok {
		return Entry{}, false, nil
	}
	e := Entry{Name: name}
	e.Command, _ = obj["command"].(string)
	e.Args = anyStrings(obj["args"])
	e.Env = anyStringMap(obj["env"])
	return e, true, nil
}

// verifyUpsert proves the edit did what it claimed and changed nothing else.
func verifyUpsert(before, after []byte, e Entry) error {
	b, err := decodeDoc(before)
	if err != nil {
		return err
	}
	a, err := decodeDoc(after)
	if err != nil {
		return fmt.Errorf("verify: result is not valid settings: %w", err)
	}
	for k, v := range b {
		if k == "agent_servers" {
			continue
		}
		if !reflect.DeepEqual(v, a[k]) {
			return fmt.Errorf("verify: key %q changed", k)
		}
	}
	bs, _ := b["agent_servers"].(map[string]any)
	as, _ := a["agent_servers"].(map[string]any)
	for k, v := range bs {
		if k == e.Name {
			continue
		}
		if !reflect.DeepEqual(v, as[k]) {
			return fmt.Errorf("verify: agent_servers.%s changed", k)
		}
	}
	if !reflect.DeepEqual(as[e.Name], normalized(e.value())) {
		return fmt.Errorf("verify: agent_servers.%s is missing or wrong", e.Name)
	}
	return nil
}

// verifyRemove proves the entry went and nothing else did.
func verifyRemove(before, after []byte, name string) error {
	b, err := decodeDoc(before)
	if err != nil {
		return err
	}
	a, err := decodeDoc(after)
	if err != nil {
		return fmt.Errorf("verify: result is not valid settings: %w", err)
	}
	for k, v := range b {
		if k == "agent_servers" {
			continue
		}
		if !reflect.DeepEqual(v, a[k]) {
			return fmt.Errorf("verify: key %q changed", k)
		}
	}
	bs, _ := b["agent_servers"].(map[string]any)
	as, _ := a["agent_servers"].(map[string]any)
	for k, v := range bs {
		if k == name {
			continue
		}
		if !reflect.DeepEqual(v, as[k]) {
			return fmt.Errorf("verify: agent_servers.%s changed", k)
		}
	}
	if _, still := as[name]; still {
		return fmt.Errorf("verify: agent_servers.%s is still present", name)
	}
	return nil
}

func readFile(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is the editor settings file supplied by config.
	if err == nil {
		return b, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return []byte("{\n}\n"), false, nil
	}
	return nil, false, err
}

// writeBackup follows the convention already present beside Zed's settings
// (settings.json.bak-YYYYMMDD-HHMMSS) rather than inventing a second one.
func writeBackup(path string, raw []byte) (string, error) {
	base := path + ".bak-" + time.Now().Format("20060102-150405")
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Stat(name); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(name, raw, 0o600); err != nil {
				return "", err
			}
			return name, nil
		}
		if i > 100 {
			return "", errors.New("could not choose a backup name")
		}
	}
}

// writeFile replaces path atomically so a failed write cannot leave Zed with a
// truncated settings file.
func writeFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// normalized round-trips a value through JSON so typed Go values ([]string,
// map[string]string) compare equal to the decoded tree ([]any, map[string]any).
func normalized(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func anyStrings(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func anyStringMap(v any) map[string]string {
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := raw[k].(string); ok {
			out[k] = s
		}
	}
	return out
}
