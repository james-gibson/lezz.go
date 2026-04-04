package selfupdate

import (
	"context"
	"fmt"
	"os"

	"github.com/creativeprojects/go-selfupdate"
)

const slug = "james-gibson/lezz.go"

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
	if !found || !release.GreaterThan(currentVersion) {
		return currentVersion, nil // already current
	}

	if err := updater.UpdateTo(ctx, release, exe); err != nil {
		return "", fmt.Errorf("apply update: %w", err)
	}

	return release.Version(), nil
}
