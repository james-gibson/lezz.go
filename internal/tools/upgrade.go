package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/creativeprojects/go-selfupdate"

	"github.com/james-gibson/lezz.go/internal/githubauth"
)

// UpgradeResult reports what upgrading one tool did. Err is set per tool so a
// caller can keep going after one tool fails and still report the rest.
type UpgradeResult struct {
	Tool      Tool
	Installed string // comparable version before the upgrade; "" if unknown/uninstalled
	Latest    string // latest release version (or the version actually installed)
	Changed   bool   // true when a download replaced the binary
	Note      string // why Installed is empty: "not installed", "unrecognized version"
	Err       error
}

// releaseManager is the release surface upgradeInto needs. ghReleaseFetcher
// implements it against GitHub; tests substitute a fake.
type releaseManager interface {
	releaseFetcher
	LatestVersion(ctx context.Context) (string, error)
}

// versionPattern finds the first semver-looking token in a tool's version
// output. Managed tools print their own name first: "adhd v0.8.6",
// "tuner 1.2.3", "ocd-smoke-alarm v0.11.0".
var versionPattern = regexp.MustCompile(`v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.\-+]+)?`)

// normalizeVersion extracts a comparable semver from raw `-v` output. It
// returns false when the output names no version at all — a commit hash from a
// source build, for example — which callers must treat as "cannot compare".
func normalizeVersion(raw string) (string, bool) {
	token := versionPattern.FindString(raw)
	if token == "" {
		return "", false
	}
	// Build metadata (a "+dirty" source build, for example) does not affect
	// semver ordering and only adds noise to what we print.
	token = strings.SplitN(token, "+", 2)[0]
	v, err := semver.NewVersion(token)
	if err != nil {
		return "", false
	}
	return v.String(), true
}

// latestVersion returns the newest published release version, normalized the
// same way as an installed version.
func (f *ghReleaseFetcher) LatestVersion(ctx context.Context) (string, error) {
	_, release, err := f.latestRelease(ctx)
	if err != nil {
		return "", err
	}
	installed, ok := normalizeVersion(release.Version())
	if !ok {
		return release.Version(), nil
	}
	return installed, nil
}

// latestRelease builds an updater and detects the tool's newest release.
func (f *ghReleaseFetcher) latestRelease(ctx context.Context) (*selfupdate.Updater, *selfupdate.Release, error) {
	updater, err := githubauth.ReleaseUpdater(f.config)
	if err != nil {
		return nil, nil, fmt.Errorf("create updater: %w", err)
	}

	release, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug(f.slug))
	if err != nil {
		return nil, nil, fmt.Errorf("detect latest release for %s: %w", f.slug, err)
	}
	if !found {
		return nil, nil, fmt.Errorf("no releases found for %s", f.slug)
	}
	return updater, release, nil
}

// InstalledVersion asks the binary in binDir for its version. ok is false when
// the tool is not installed; an installed binary whose version cannot be read
// or parsed returns ("", true).
func InstalledVersion(ctx context.Context, binDir, name string) (version string, ok bool) {
	path := filepath.Join(binDir, name)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	// A wedged binary must not wedge `lezz update`: asking for a version is a
	// bounded question.
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, path, "-v").Output() //nolint:gosec // path is ~/.lezz/bin/<name>, a managed binary
	if err != nil {
		return "", true
	}
	version, parsed := normalizeVersion(string(out))
	if !parsed {
		return "", true
	}
	return version, true
}

// Upgrade brings one managed tool up to its newest GitHub release. It reports
// "up to date" without downloading when the installed version is current.
// lezz itself is not upgraded here — that path is `lezz update`.
func Upgrade(ctx context.Context, tool Tool) (UpgradeResult, error) {
	binDir, err := BinDir()
	if err != nil {
		return UpgradeResult{Tool: tool}, err
	}
	if mkErr := os.MkdirAll(binDir, 0o750); mkErr != nil {
		return UpgradeResult{Tool: tool}, fmt.Errorf("create bin dir: %w", mkErr)
	}
	return upgradeInto(ctx, tool, &ghReleaseFetcher{slug: tool.GithubSlug}, binDir)
}

// UpgradeAll upgrades the named tools, or every daemonisable managed tool
// except lezz when names is empty. It never stops at the first failure: each
// result carries its own error.
func UpgradeAll(ctx context.Context, names []string) ([]UpgradeResult, error) {
	selected, err := selectTools(names)
	if err != nil {
		return nil, err
	}

	binDir, err := BinDir()
	if err != nil {
		return nil, err
	}
	if mkErr := os.MkdirAll(binDir, 0o750); mkErr != nil {
		return nil, fmt.Errorf("create bin dir: %w", mkErr)
	}

	results := make([]UpgradeResult, 0, len(selected))
	for _, tool := range selected {
		result, upErr := upgradeInto(ctx, tool, &ghReleaseFetcher{slug: tool.GithubSlug}, binDir)
		result.Err = upErr
		results = append(results, result)
	}
	return results, nil
}

// upgradeInto is the testable core of Upgrade.
func upgradeInto(ctx context.Context, tool Tool, rm releaseManager, binDir string) (UpgradeResult, error) {
	result := UpgradeResult{Tool: tool}

	latest, err := rm.LatestVersion(ctx)
	if err != nil {
		return result, err
	}
	result.Latest = latest

	installed, ok := InstalledVersion(ctx, binDir, tool.Name)
	result.Installed = installed
	switch {
	case !ok:
		result.Note = "not installed"
	case installed == "":
		result.Note = "unrecognized version"
	default:
		newer, cmpErr := releaseIsNewer(latest, installed)
		if cmpErr != nil {
			return result, cmpErr
		}
		if !newer {
			return result, nil
		}
	}

	ver, err := rm.FetchLatest(ctx, filepath.Join(binDir, tool.Name))
	if err != nil {
		return result, fmt.Errorf("download %s: %w", tool.Name, err)
	}
	result.Changed = true
	if norm, ok := normalizeVersion(ver); ok {
		result.Latest = norm
	} else if ver != "" {
		result.Latest = ver
	}
	return result, nil
}

// releaseIsNewer reports whether latest is a newer release than installed. Both
// must already be normalized semver.
func releaseIsNewer(latest, installed string) (bool, error) {
	newer, err := semver.NewVersion(latest)
	if err != nil {
		return false, fmt.Errorf("parse release version %q: %w", latest, err)
	}
	current, err := semver.NewVersion(installed)
	if err != nil {
		return false, fmt.Errorf("parse installed version %q: %w", installed, err)
	}
	return newer.GreaterThan(current), nil
}

// selectTools resolves the tool names given on the command line. No names means
// every managed tool except lezz, which updates itself through `lezz update`.
func selectTools(names []string) ([]Tool, error) {
	if len(names) == 0 {
		selected := make([]Tool, 0, len(Registry))
		for _, tool := range Registry {
			if tool.Name == "lezz" {
				continue
			}
			selected = append(selected, tool)
		}
		return selected, nil
	}

	selected := make([]Tool, 0, len(names))
	for _, name := range names {
		tool, ok := Lookup(name)
		if !ok {
			return nil, fmt.Errorf("unknown tool %q\nmanaged tools: %v", name, Names())
		}
		if tool.Name == "lezz" {
			return nil, fmt.Errorf("lezz updates itself: run lezz update")
		}
		selected = append(selected, tool)
	}
	return selected, nil
}
