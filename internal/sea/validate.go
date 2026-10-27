//
//  validate.go
//  lezz.go
//
//  Requests name files inside the app's cluster root; they never name argv.
//  The supervisor resolves every path against the root the app announced, so a
//  request cannot start an alarm against a config outside the app's own
//  instance directory. The argv is then built here from a fixed template.
//

package sea

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Binder confines requested paths to one cluster root.
type Binder struct {
	Root string
}

// NewBinder returns a binder for root, which must be absolute.
func NewBinder(root string) (*Binder, error) {
	if root == "" {
		return nil, fmt.Errorf("cluster root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("cluster root %q: %w", root, err)
	}
	return &Binder{Root: filepath.Clean(abs)}, nil
}

// Path resolves raw under the root and checks its extension. It is a lexical
// check: a symlink created inside the root could still point out of it. The
// root belongs to the signed app, so the remaining boundary is the user
// account, which this channel does not cross.
func (b *Binder) Path(raw, extension string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}
	clean := filepath.Clean(abs)
	rel, err := filepath.Rel(b.Root, clean)
	if err != nil {
		return "", fmt.Errorf("resolve %q against %s: %w", raw, b.Root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s is outside the app's cluster root", clean)
	}
	if extension != "" && !strings.HasSuffix(strings.ToLower(clean), extension) {
		return "", fmt.Errorf("%s does not end in %s", clean, extension)
	}
	return clean, nil
}

// Config validates a derived alarm config before lezz.go runs it.
//
// The three runtime paths must be absolute and under the root. That is what
// makes the file the app's own instance config rather than an arbitrary YAML
// that writes over the checkout or the home directory.
func (b *Binder) Config(raw string) (string, error) {
	path, err := b.Path(raw, ".yaml")
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("config %s: %w", path, err)
	}
	if info.Size() > 1<<20 {
		return "", fmt.Errorf("config %s is larger than 1 MiB", path)
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is confined to the app's cluster root above
	if err != nil {
		return "", fmt.Errorf("read config %s: %w", path, err)
	}

	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := runtimePath(line)
		if !ok {
			continue
		}
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("config %s: %s is not absolute", path, key)
		}
		resolved, err := b.Path(value, "")
		if err != nil {
			return "", fmt.Errorf("config %s: %s: %w", path, key, err)
		}
		if resolved == b.Root {
			return "", fmt.Errorf("config %s: %s names the root, not a file", path, key)
		}
		seen[key] = true
	}
	for _, key := range []string{"state_dir", "lock_file", "baseline_file"} {
		if !seen[key] {
			return "", fmt.Errorf("config %s does not declare runtime.%s", path, key)
		}
	}
	return path, nil
}

// runtimePath reads an indented `key: "value"` or `key: value` line for the
// three runtime paths ScenarioConfig writes.
func runtimePath(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	colon := strings.Index(trimmed, ":")
	if colon <= 0 {
		return "", "", false
	}
	key = trimmed[:colon]
	switch key {
	case "state_dir", "lock_file", "baseline_file":
	default:
		return "", "", false
	}
	value = strings.TrimSpace(trimmed[colon+1:])
	if hash := strings.Index(value, "#"); hash >= 0 {
		value = strings.TrimSpace(value[:hash])
	}
	value = strings.Trim(value, `"`)
	if value == "" {
		return "", "", false
	}
	return key, value, true
}
