package sea

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/james-gibson/lezz.go/internal/appbundle"
)

// install places a verified bundle in ~/Applications without ever overwriting
// a running one: copy to a staging name, swap by rename, and only then drop the
// previous copy. A failed rename restores what was there.
func (e *env) install(ctx context.Context, srcApp string) (string, error) {
	if pids, err := e.running(filepath.Join(e.home, "Applications", AppName)); err == nil && len(pids) > 0 {
		return "", fmt.Errorf("lezz.sea is running (pid %v): quit it before reinstalling", pids)
	}

	dir := filepath.Join(e.home, "Applications")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	dest := filepath.Join(dir, AppName)

	staging := fmt.Sprintf("%s.new-%d", dest, os.Getpid())
	if err := os.RemoveAll(staging); err != nil {
		return "", fmt.Errorf("clear %s: %w", staging, err)
	}
	if err := e.copyDir(srcApp, staging); err != nil {
		return "", fmt.Errorf("copy bundle into place: %w", err)
	}

	previous := ""
	if _, err := os.Stat(dest); err == nil {
		previous = fmt.Sprintf("%s.old-%d", dest, os.Getpid())
		if err := os.RemoveAll(previous); err != nil {
			return "", fmt.Errorf("clear %s: %w", previous, err)
		}
		if err := os.Rename(dest, previous); err != nil {
			return "", fmt.Errorf("move the installed bundle aside: %w", err)
		}
	}
	if err := os.Rename(staging, dest); err != nil {
		if previous != "" {
			_ = os.Rename(previous, dest) // best effort: restore what was there
		}
		return "", fmt.Errorf("install %s: %w", dest, err)
	}
	if previous != "" {
		_ = os.RemoveAll(previous)
	}

	// The bytes have been verified above, so a quarantine flag inherited from
	// the download must not make the first launch fail. This runs only after
	// inspect() has accepted the bundle.
	if _, err := e.run(ctx, "xattr", "-dr", "com.apple.quarantine", dest); err != nil {
		e.logf("warning: could not clear the quarantine flag: %v", err)
	}
	if err := e.register(dest); err != nil {
		e.logf("warning: could not register the bundle with LaunchServices: %v", err)
	}
	return dest, nil
}

// execRunner runs a command and returns its combined output. codesign -dv writes
// its description to stderr, so stdout alone is not enough.
func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: fixed binaries, argv-only, no shell.
	return cmd.CombinedOutput()
}

// dittoCopy preserves the bundle's metadata and extended attributes, which is
// what keeps the signature valid across the copy.
func dittoCopy(src, dst string) error {
	out, err := exec.Command("ditto", src, dst).CombinedOutput() //nolint:gosec // G204: fixed binary, argv-only, no shell.
	if err != nil {
		return fmt.Errorf("ditto: %w\n%s", err, string(out))
	}
	return nil
}

func runningPIDs(appPath string) ([]int, error) {
	return appbundle.RunningPIDs(appPath)
}

func registerBundle(appPath string) error {
	return appbundle.Register(appPath)
}
