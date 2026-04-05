package selfupdate

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/creativeprojects/go-selfupdate"
)

const slug = "james-gibson/lezz.go"

// isValidSemver returns true when v is a clean release semver tag that
// go-selfupdate can compare reliably.  It rejects:
//   - Go pseudo-versions (vX.Y.Z-YYYYMMDDHHMMSS-COMMIT) — two hyphen groups
//     where the second segment is a 14-digit timestamp
//   - Dirty local builds (+dirty build metadata)
//   - Anything that doesn't start with a digit after stripping the "v" prefix
func isValidSemver(v string) bool {
	v = strings.TrimPrefix(v, "v")
	if v == "" || v[0] < '0' || v[0] > '9' {
		return false
	}
	// Dirty build metadata — never a release.
	if strings.Contains(v, "+dirty") {
		return false
	}
	// Go pseudo-version: vX.Y.Z-0.YYYYMMDDHHMMSS-COMMIT
	// The middle segment is the epoch-relative minor version followed by the
	// 14-digit UTC timestamp, e.g. "0.20260404234801".  Strip build metadata
	// first, split on "-" into at most three parts, then check that the second
	// part ends with a 14-digit timestamp (digits only after the last ".").
	bare := strings.SplitN(v, "+", 2)[0] // strip "+..." if present
	parts := strings.SplitN(bare, "-", 3) // base, middle?, commit?
	if len(parts) == 3 {
		ts := parts[1]
		if idx := strings.LastIndexByte(ts, '.'); idx >= 0 {
			ts = ts[idx+1:]
		}
		if len(ts) == 14 && isAllDigits(ts) {
			return false // Go pseudo-version
		}
	}
	return true
}

// isAllDigits reports whether s consists entirely of ASCII decimal digits.
func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// newUpdater creates an unauthenticated updater for public GitHub repos.
// go-selfupdate inherits $GITHUB_TOKEN from the environment when no token is
// configured; a stale or unrelated token causes 401 on public repos, so we
// clear it before constructing the updater.
func newUpdater() (*selfupdate.Updater, error) {
	old, had := os.LookupEnv("GITHUB_TOKEN")
	if had {
		_ = os.Unsetenv("GITHUB_TOKEN")
		defer func() { _ = os.Setenv("GITHUB_TOKEN", old) }()
	}
	return selfupdate.NewUpdater(selfupdate.Config{})
}

// Check returns the latest available version string without applying it.
func Check(ctx context.Context, currentVersion string) (latest string, hasUpdate bool, err error) {
	updater, err := newUpdater()
	if err != nil {
		return "", false, fmt.Errorf("create updater: %w", err)
	}

	release, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug(slug))
	if err != nil {
		return "", false, fmt.Errorf("detect latest: %w", err)
	}
	if !found {
		return currentVersion, false, nil
	}

	if !isValidSemver(currentVersion) {
		// dev/dirty/pseudo-version build — always update to the latest release.
		return release.Version(), true, nil
	}
	return release.Version(), release.GreaterThan(currentVersion), nil
}

// Apply downloads and atomically installs the latest lezz release over the
// running binary.  Returns the new version string.
func Apply(ctx context.Context, currentVersion string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}

	updater, err := newUpdater()
	if err != nil {
		return "", fmt.Errorf("create updater: %w", err)
	}

	release, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug(slug))
	if err != nil {
		return "", fmt.Errorf("detect latest: %w", err)
	}
	if !found {
		return currentVersion, nil
	}
	if isValidSemver(currentVersion) && !release.GreaterThan(currentVersion) {
		return currentVersion, nil // already current
	}

	if err := updater.UpdateTo(ctx, release, exe); err != nil {
		return "", fmt.Errorf("apply update: %w", err)
	}

	return release.Version(), nil
}
