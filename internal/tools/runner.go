package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// BinDir returns the lezz tool cache directory: ~/.lezz/bin
func BinDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".lezz", "bin"), nil
}

// Find locates the binary for a managed tool.
// Search order: ~/.lezz/bin/<name>, then PATH.
func Find(name string) (string, error) {
	if binDir, err := BinDir(); err == nil {
		cached := filepath.Join(binDir, name)
		if _, err := os.Stat(cached); err == nil {
			return cached, nil
		}
	}

	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("%q not found in ~/.lezz/bin or PATH — run: lezz install %s", name, name)
}

// Run replaces the current process with the named managed tool.
// Any args after the tool name are forwarded unchanged.
func Run(name string, args []string) error {
	if _, ok := Lookup(name); !ok {
		return fmt.Errorf("unknown tool %q\nmanaged tools: %v", name, Names())
	}

	binary, err := Find(name)
	if err != nil {
		return err
	}

	argv := append([]string{binary}, args...)
	return syscall.Exec(binary, argv, os.Environ()) //nolint:gosec // binary path is resolved via Find() from the managed tool registry
}
