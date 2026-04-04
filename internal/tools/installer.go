package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creativeprojects/go-selfupdate"
)

// releaseFetcher is the minimal surface installViaRelease needs.
// The real implementation calls GitHub; tests swap in a fileFetcher.
type releaseFetcher interface {
	FetchLatest(ctx context.Context, dest string) (version string, err error)
}

// ghReleaseFetcher fetches from GitHub releases using go-selfupdate.
// config.Source may be set to a mock in tests; the zero value uses real GitHub.
type ghReleaseFetcher struct {
	slug   string
	config selfupdate.Config
}

func (f *ghReleaseFetcher) FetchLatest(ctx context.Context, dest string) (string, error) {
	// update.Apply (used internally by UpdateTo) requires the target file to exist
	// so it can atomically rename old → .old and new → target.  Create an empty
	// placeholder on a fresh install so the rename does not fail with ENOENT.
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		if wErr := os.WriteFile(dest, []byte{}, 0o600); wErr != nil {
			return "", fmt.Errorf("create install placeholder: %w", wErr)
		}
	}

	updater, err := selfupdate.NewUpdater(f.config)
	if err != nil {
		return "", fmt.Errorf("create updater: %w", err)
	}

	release, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug(f.slug))
	if err != nil {
		return "", fmt.Errorf("detect latest release for %s: %w", f.slug, err)
	}
	if !found {
		return "", fmt.Errorf("no releases found for %s", f.slug)
	}

	if err := updater.UpdateTo(ctx, release, dest); err != nil {
		return "", fmt.Errorf("download %s: %w", release.Version(), err)
	}

	return release.Version(), nil
}

// fileFetcher installs by copying a local file — used in tests.
type fileFetcher struct {
	src     string // path to the binary to "install"
	version string
}

func (f *fileFetcher) FetchLatest(_ context.Context, dest string) (version string, err error) {
	in, err := os.Open(f.src)
	if err != nil {
		return "", fmt.Errorf("open source: %w", err)
	}
	defer in.Close() //nolint:errcheck // read-only file; close error is inconsequential

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755) //nolint:gosec // dest is a controlled path under ~/.lezz/bin
	if err != nil {
		return "", fmt.Errorf("create dest: %w", err)
	}
	defer func() {
		if cErr := out.Close(); cErr != nil && err == nil {
			err = fmt.Errorf("close dest: %w", cErr)
		}
	}()

	if _, copyErr := io.Copy(out, in); copyErr != nil {
		return "", fmt.Errorf("copy: %w", copyErr)
	}

	return f.version, nil
}

// Install places the tool binary in ~/.lezz/bin/<name>.
//
// Strategy selection:
//  1. Download the pre-built GitHub release asset (same source as `lezz update`).
//  2. If no release is found and Go is available, fall back to go install.
func Install(ctx context.Context, tool Tool) (ver string, err error) {
	binDir, err := BinDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}

	ver, err = installViaFetcher(ctx, &ghReleaseFetcher{slug: tool.GithubSlug}, tool.Name, binDir)
	if err == nil {
		return ver, nil
	}

	if _, goErr := exec.LookPath("go"); goErr == nil {
		return installViaGoInstall(ctx, tool, binDir)
	}
	return "", err
}

// installViaGoInstall runs `go install <slug>/cmd/<name>@latest` and symlinks
// the result into binDir so lezz run finds it without PATH gymnastics.
func installViaGoInstall(ctx context.Context, tool Tool, binDir string) (string, error) {
	module := "github.com/" + tool.GithubSlug + "/cmd/" + tool.Name + "@latest"

	cmd := exec.CommandContext(ctx, "go", "install", module) //nolint:gosec // module path is constructed from the registry-defined tool slug
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go install %s: %w", module, err)
	}

	return linkFromGOPATH(ctx, tool.Name, binDir)
}

// linkFromGOPATH reads GOPATH via `go env`, finds the binary there, and
// symlinks it into binDir.  Extracted so tests can call it directly after
// pre-placing a binary in a fake GOPATH.
func linkFromGOPATH(ctx context.Context, name, binDir string) (string, error) {
	gopathOut, err := exec.CommandContext(ctx, "go", "env", "GOPATH").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOPATH: %w", err)
	}
	gopath := strings.TrimSpace(string(gopathOut))
	src := filepath.Join(gopath, "bin", name)

	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("binary not found at %s (go install may have failed silently)", src)
	}

	dest := filepath.Join(binDir, name)
	_ = os.Remove(dest)
	if err := os.Symlink(src, dest); err != nil {
		return "", fmt.Errorf("symlink %s → %s: %w", src, dest, err)
	}

	return "latest", nil
}

// installViaFetcher is the testable core of installViaRelease.
func installViaFetcher(ctx context.Context, fetcher releaseFetcher, name, binDir string) (string, error) {
	dest := filepath.Join(binDir, name)
	return fetcher.FetchLatest(ctx, dest)
}
