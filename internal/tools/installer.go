package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creativeprojects/go-selfupdate"

	"github.com/james-gibson/lezz.go/internal/githubauth"
)

// ErrNoReleaseAsset means the repository publishes nothing this platform can
// download — no releases at all, or none with a matching asset. It is the only
// outcome Install may answer by building from source: an API error is not the
// same thing and must reach the user (james-gibson/lezz.go#36).
var ErrNoReleaseAsset = errors.New("no release asset for this platform")

// checksumConfig makes the release's checksums.txt mandatory. go-selfupdate
// validates a download only when a validator is configured (update.go:
// `if up.validator != nil`), so the zero value used to mean "verify nothing";
// a release without checksums.txt now fails detection rather than installing
// unchecked bytes.
func checksumConfig() selfupdate.Config {
	return selfupdate.Config{Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"}}
}

// sourceInstallAllowed reports whether the unpinned `go install @latest`
// fallback may run. It is off by default: that path bypasses the release
// checksum and the signature, and it builds whatever upstream main happens to
// be at that moment.
func sourceInstallAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEZZ_ALLOW_SOURCE_INSTALL"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

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

func (f *ghReleaseFetcher) FetchLatest(ctx context.Context, dest string) (version string, err error) {
	// update.Apply (used internally by UpdateTo) requires the target file to exist
	// so it can atomically rename old → .old and new → target.  Create an empty
	// placeholder on a fresh install so the rename does not fail with ENOENT.
	placeholder := false
	if _, statErr := os.Stat(dest); os.IsNotExist(statErr) {
		if wErr := os.WriteFile(dest, []byte{}, 0o600); wErr != nil {
			return "", fmt.Errorf("create install placeholder: %w", wErr)
		}
		placeholder = true
		// A failed download must not leave a 0-byte "binary" in ~/.lezz/bin,
		// where lezz would later try to run it.
		defer func() {
			if placeholder && err != nil {
				_ = os.Remove(dest)
			}
		}()
	}

	updater, err := githubauth.ReleaseUpdater(f.config)
	if err != nil {
		return "", fmt.Errorf("create updater: %w", err)
	}

	release, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug(f.slug))
	if err != nil {
		return "", fmt.Errorf("detect latest release for %s: %w", f.slug, err)
	}
	if !found {
		return "", fmt.Errorf("%w: no releases found for %s", ErrNoReleaseAsset, f.slug)
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
//  1. Download the pre-built GitHub release asset, verified against the
//     release's checksums.txt (same source as `lezz update`).
//  2. Only when that repository publishes no asset for this platform, Go is
//     available, and LEZZ_ALLOW_SOURCE_INSTALL=1, build with go install. That
//     path is unpinned and unverified, so it is never silent.
//
// An API failure (401, 403, rate limit, network) is reported, never converted
// into a quiet source build: a stale $GITHUB_TOKEN used to make every install
// fall through to `go install` and report "latest" (james-gibson/lezz.go#36).
func Install(ctx context.Context, tool Tool) (ver string, err error) {
	binDir, err := BinDir()
	if err != nil {
		return "", err
	}
	if mkErr := os.MkdirAll(binDir, 0o750); mkErr != nil {
		return "", fmt.Errorf("create bin dir: %w", mkErr)
	}
	return installInto(ctx, &ghReleaseFetcher{slug: tool.GithubSlug, config: checksumConfig()}, tool, binDir)
}

// installInto is the testable core of Install: fetch from fetcher, and fall back
// to a source build only when there is genuinely no asset to fetch.
func installInto(ctx context.Context, fetcher releaseFetcher, tool Tool, binDir string) (string, error) {
	ver, err := installViaFetcher(ctx, fetcher, tool.Name, binDir)
	switch {
	case err == nil:
		if sigErr := verifyInstalledBinary(ctx, filepath.Join(binDir, tool.Name), defaultWarn); sigErr != nil {
			return "", sigErr
		}
		return ver, nil
	case errors.Is(err, ErrNoReleaseAsset):
		if _, goErr := exec.LookPath("go"); goErr == nil && sourceInstallAllowed() {
			return installViaGoInstall(ctx, tool, binDir)
		}
		return "", fmt.Errorf("%w\n  note: a source build is available with LEZZ_ALLOW_SOURCE_INSTALL=1, but it is unpinned and unverified", err)
	default:
		return "", authHint(err)
	}
}

// authHint appends the one action that fixes a rejected credential.
func authHint(err error) error {
	msg := strings.ToLower(err.Error())
	for _, signal := range []string{"401", "403", "bad credentials", "rate limit"} {
		if strings.Contains(msg, signal) {
			return fmt.Errorf("%w\n  hint: run `lezz auth github` so downloads use your GitHub account", err)
		}
	}
	return err
}

// installViaGoInstall runs `go install <slug>/cmd/<name>@latest` and symlinks
// the result into binDir so lezz run finds it without PATH gymnastics. It is
// reached only through sourceInstallAllowed; nothing here verifies a checksum
// or a signature, because there is nothing published to verify against.
func installViaGoInstall(ctx context.Context, tool Tool, binDir string) (string, error) {
	module := "github.com/" + tool.GithubSlug + "/cmd/" + tool.Name + "@latest"

	cmd := exec.CommandContext(ctx, "go", "install", module) //nolint:gosec // module path is constructed from the registry-defined tool slug
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go install %s: %w", module, err)
	}

	if _, err := linkFromGOPATH(ctx, tool.Name, binDir); err != nil {
		return "", err
	}

	// A source build has a module version in its build info; report it so
	// "installed adhd latest" cannot be mistaken for a release artifact.
	if ver := moduleVersion(ctx, filepath.Join(binDir, tool.Name)); ver != "" {
		return ver, nil
	}
	return "latest (from source)", nil
}

// moduleVersion reads the main module version from a binary's build info:
//
//	mod	github.com/james-gibson/adhd	v0.8.6	h1:...
//
// It returns "" when the binary carries no module version (a `go build` inside
// a checkout records "(devel)" or nothing usable).
func moduleVersion(ctx context.Context, path string) string {
	//nolint:gosec // G204: path is ~/.lezz/bin/<name>, a managed binary
	out, err := exec.CommandContext(ctx, "go", "version", "-m", path).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "mod" {
			if v := fields[2]; strings.HasPrefix(v, "v") && strings.Count(v, ".") >= 2 {
				return v
			}
		}
	}
	return ""
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
