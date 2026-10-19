// Package credential stores small secrets behind a platform-appropriate
// backend. macOS uses the login Keychain; other platforms currently use a
// 0600 file under ~/.lezz/auth. The interface is deliberately small so a
// native helper (SecItemAdd with access control) or a Linux Secret Service /
// Windows Credential Manager backend can replace the fallback.
package credential

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNotFound means the requested item does not exist.
var ErrNotFound = errors.New("credential not found")

// Store is a named-secret store. Service and account form the key; the secret
// is never returned from Set or Delete.
type Store interface {
	Set(service, account, secret string) error
	Get(service, account string) (string, error)
	Delete(service, account string) error
	Exists(service, account string) (bool, error)
	// Ref returns the seed auth.ref form for the item (keychain:NAME or
	// file:/path). It does not reveal the secret.
	Ref(service, account string) string
	Kind() string
}

// Default chooses Keychain on macOS and a 0600 file elsewhere.
func Default() Store {
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("security"); err == nil {
			return Keychain{}
		}
	}
	return FileStore{}
}

// New selects a backend by name: auto, keychain, or file.
func New(kind string) (Store, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "auto":
		return Default(), nil
	case "keychain":
		if runtime.GOOS != "darwin" {
			return nil, fmt.Errorf("the keychain store is only supported on macOS; use --store file")
		}
		if _, err := exec.LookPath("security"); err != nil {
			return nil, fmt.Errorf("keychain store: security command not found")
		}
		return Keychain{}, nil
	case "file":
		return FileStore{}, nil
	default:
		return nil, fmt.Errorf("unknown credential store %q (use auto, keychain, or file)", kind)
	}
}

// Keychain stores generic passwords in the macOS login Keychain via security(1).
type Keychain struct{}

// Kind reports the backend name ("keychain").
func (Keychain) Kind() string { return "keychain" }

// Ref returns the seed auth.ref for the item as keychain:SERVICE. The account
// is not part of the ref; this store's callers use a fixed account.
func (Keychain) Ref(service, _ string) string { return "keychain:" + service }

// Set adds or replaces a generic password in the login Keychain.
func (Keychain) Set(service, account, secret string) error {
	// security(1) only accepts the secret as an argument (or an interactive
	// prompt). This briefly exposes it in the process table; the native helper
	// will replace this with SecItemAdd and an access-control object.
	if out, err := exec.Command("security", "add-generic-password", //nolint:gosec // G204: fixed binary, argv-only, no shell.
		"-U", "-s", service, "-a", account, "-w", secret).CombinedOutput(); err != nil {
		return fmt.Errorf("keychain set %s: %w: %s", service, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Get returns the stored secret, or ErrNotFound when the item is absent.
func (Keychain) Get(service, account string) (string, error) {
	out, err := exec.Command("security", "find-generic-password", //nolint:gosec // G204: fixed binary, argv-only, no shell.
		"-s", service, "-a", account, "-w").Output()
	if err != nil {
		if isKeychainNotFound(err, out) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keychain get %s: %w", service, err)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// Delete removes the item, returning ErrNotFound when it is already absent.
func (Keychain) Delete(service, account string) error {
	out, err := exec.Command("security", "delete-generic-password", //nolint:gosec // G204: fixed binary, argv-only, no shell.
		"-s", service, "-a", account).CombinedOutput()
	if err != nil {
		if isKeychainNotFound(err, out) {
			return ErrNotFound
		}
		return fmt.Errorf("keychain delete %s: %w: %s", service, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Exists checks for the item without retrieving its secret.
func (Keychain) Exists(service, account string) (bool, error) {
	_, err := exec.Command("security", "find-generic-password", //nolint:gosec // G204: fixed binary, argv-only, no shell.
		"-s", service, "-a", account).Output()
	if err != nil {
		if isKeychainNotFound(err, nil) {
			return false, nil
		}
		return false, fmt.Errorf("keychain exists %s: %w", service, err)
	}
	return true, nil
}

func isKeychainNotFound(err error, out []byte) bool {
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 44 {
		return true
	}
	return strings.Contains(strings.ToLower(string(out)), "could not be found")
}

// FileStore stores each secret in a 0600 file. It is the fallback for systems
// without a platform store; it is not a substitute for one.
type FileStore struct {
	Dir string // default ~/.lezz/auth
}

// Kind reports the backend name, including the on-disk permission it enforces.
func (f FileStore) Kind() string { return "file (0600)" }

func (f FileStore) dir() (string, error) {
	if f.Dir != "" {
		return f.Dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".lezz", "auth"), nil
}

func (f FileStore) path(service, account string) (string, error) {
	dir, err := f.dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, safe(service)+"__"+safe(account)+".secret"), nil
}

// Ref returns the seed auth.ref for the item as file:/absolute/path. It does
// not check that the file exists.
func (f FileStore) Ref(service, account string) string {
	p, err := f.path(service, account)
	if err != nil {
		return "file:"
	}
	return "file:" + p
}

// Set writes the secret to a 0600 file, replacing any existing value atomically.
func (f FileStore) Set(service, account, secret string) error {
	p, err := f.path(service, account)
	if err != nil {
		return err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(p), 0o700); mkdirErr != nil {
		return mkdirErr
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".secret-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(secret); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, p); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// Get returns the stored secret, or ErrNotFound when the file is absent.
func (f FileStore) Get(service, account string) (string, error) {
	p, err := f.path(service, account)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p) //nolint:gosec // G304: path is built by path() from sanitized service/account names under Dir.
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Exists reports whether the secret file is present without reading it.
func (f FileStore) Exists(service, account string) (bool, error) {
	p, err := f.path(service, account)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes the secret file, returning ErrNotFound when it is absent.
func (f FileStore) Delete(service, account string) error {
	p, err := f.path(service, account)
	if err != nil {
		return err
	}
	if err := os.Remove(p); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

func safe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}
