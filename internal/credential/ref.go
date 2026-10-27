package credential

import (
	"fmt"
	"os"
	"strings"
)

// Resolve reads a secret named by a reference, without ever putting the value
// on a command line.
//
// Supported forms:
//
//	env:NAME               the process environment
//	file:/abs/path         a 0600 file
//	keychain:SERVICE       any account on a generic-password item
//	keychain:SERVICE/ACCT  one specific account
//
// The forms match `lezz seed init --auth-ref`, so a ref that identifies a seed
// credential also identifies a bridge secret.
//
// This is the only place a stored secret becomes a string. Callers must pass
// the result straight into a child environment (or an HTTP header), never into
// argv, a log line, or a config file.
func Resolve(ref string) (string, error) {
	kind, rest, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || rest == "" {
		return "", fmt.Errorf("secret reference %q: expected env:NAME, file:/path, or keychain:SERVICE", ref)
	}
	switch strings.ToLower(kind) {
	case "env":
		value := os.Getenv(rest)
		if value == "" {
			return "", fmt.Errorf("env:%s: %w", rest, ErrNotFound)
		}
		return value, nil
	case "file":
		return resolveFile(ref, rest)
	case "keychain":
		service, account, hasAccount := strings.Cut(rest, "/")
		if service == "" {
			return "", fmt.Errorf("secret reference %q: keychain service is empty", ref)
		}
		if hasAccount {
			return Keychain{}.Get(service, account)
		}
		return Keychain{}.GetService(service)
	default:
		return "", fmt.Errorf("secret reference %q: unsupported scheme %q (use env, file, or keychain)", ref, kind)
	}
}

func resolveFile(ref, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file:%s: %w", path, ErrNotFound)
		}
		return "", fmt.Errorf("secret reference %q: %w", ref, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("secret reference %q: %s is a directory", ref, path)
	}
	// A secret another user can read is not a secret. Refuse rather than warn:
	// silently accepting a 0644 file is how a credential leaks into a backup.
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return "", fmt.Errorf("secret reference %q: %s is %04o; must not be readable by group or others (chmod 0600)", ref, path, mode)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied ref, permission-checked above.
	if err != nil {
		return "", fmt.Errorf("secret reference %q: %w", ref, err)
	}
	value := strings.TrimRight(string(data), "\n")
	if value == "" {
		return "", fmt.Errorf("file:%s: %w", path, ErrNotFound)
	}
	return value, nil
}
