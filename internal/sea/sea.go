// Package sea installs and launches the lezz.sea dashboard.
//
// The dashboard is published as a signed macOS app bundle on the `dev`
// prerelease of james-gibson/lezz.sea. This package is the only place in lezz
// that fetches it, and it fetches it the way an installer should:
//
//   - HTTPS only, with every redirect checked against a host allowlist, so a
//     compromised redirect cannot move the download to another origin.
//   - The archive's SHA-256 must match the published manifest, so a truncated
//     or substituted body is rejected before anything is unpacked.
//   - The unpacked bundle must carry a Developer ID signature from team
//     288ZTJGXX9, the hardened runtime, and a secure timestamp. The signature,
//     not the checksum, is the trust anchor: the checksum comes from the same
//     origin as the archive, while the signature comes from Apple's chain.
//   - Notarization is required by default. A signed but un-notarized build is
//     refused with a message naming the reason and the explicit escape hatch
//     (Options.AllowUnnotarized), because macOS would block its first launch
//     anyway.
//   - Nothing is installed until every check above has passed, and the install
//     itself is a staged copy plus a rename, never an in-place overwrite of a
//     bundle that is running.
package sea

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	// Slug is the repository that publishes the dashboard.
	Slug = "james-gibson/lezz.sea"
	// Channel is the prerelease the dev builds are published to. Its assets
	// are replaced on every publish, which is what makes the URL stable.
	Channel = "dev"
	// AppName is the bundle name inside the release archive.
	AppName = "lezz.sea.app"
	// BundleID is the dashboard's bundle identifier.
	BundleID = "co.jamesgibson.LezzSea"
	// TeamID is the Apple team that signs releases.
	TeamID = "288ZTJGXX9"
	// Authority is the leaf certificate the bundle must be signed with.
	Authority = "Developer ID Application: James Gibson (288ZTJGXX9)"
	// DefaultBaseURL is where the release assets live. Tests replace it.
	DefaultBaseURL = "https://github.com/" + Slug + "/releases/download"

	// maxManifest caps the manifest read; it is a few hundred bytes.
	maxManifest = 1 << 20
	// maxArchive caps the download when the manifest carries no size.
	maxArchive = 256 << 20
)

// Options controls Ensure. The zero value is the safe default: no network if a
// verified copy is already installed, notarization required, no forced
// reinstall.
type Options struct {
	// Home is the user's home directory; empty means os.UserHomeDir.
	Home string
	// Channel is the release channel, default Channel.
	Channel string
	// Force reinstalls even when a verified copy is already installed.
	Force bool
	// AllowUnnotarized installs a signed build that has no notarization
	// ticket. It exists for the window before a notarized build is published;
	// it prints a warning and is never implied.
	AllowUnnotarized bool
	// Log receives progress lines; nil discards them.
	Log func(format string, args ...any)
}

// Result describes what Ensure left installed.
type Result struct {
	Path       string
	Version    string
	Build      string
	Notarized  bool
	Downloaded bool
}

// Info is the observable state of an installed bundle.
type Info struct {
	Path            string
	BundleID        string
	Version         string
	Build           string
	Authority       string
	TeamID          string
	HardenedRuntime bool
	Timestamped     bool
	Notarized       bool
}

// env is the seam between this package and the machine. Every side effect —
// running codesign, copying a bundle, fetching a URL — goes through it, so the
// whole flow can be exercised in tests without a network or a signed app.
type env struct {
	home     string
	goos     string
	channel  string
	base     string
	run      runner
	copyDir  func(src, dst string) error
	running  func(appPath string) ([]int, error)
	client   *httpClient
	register func(appPath string) error
	logf     func(format string, args ...any)
}

type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func newEnv(opts Options) (*env, error) {
	home := opts.Home
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home directory: %w", err)
		}
		home = h
	}
	channel := opts.Channel
	if channel == "" {
		channel = Channel
	}
	if !validChannel(channel) {
		return nil, fmt.Errorf("invalid channel %q: use letters, digits, '.', '_' or '-'", channel)
	}
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &env{
		home:     home,
		goos:     runtime.GOOS,
		channel:  channel,
		base:     DefaultBaseURL,
		run:      execRunner,
		copyDir:  dittoCopy,
		running:  runningPIDs,
		client:   newHTTPClient(),
		register: registerBundle,
		logf:     logf,
	}, nil
}

// validChannel keeps a typed channel out of the URL path.
func validChannel(channel string) bool {
	if channel == "" || len(channel) > 64 {
		return false
	}
	for _, r := range channel {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// Ensure returns a verified installed dashboard, downloading and installing it
// from the channel when it is missing (or when Options.Force is set).
func Ensure(ctx context.Context, opts Options) (Result, error) {
	e, err := newEnv(opts)
	if err != nil {
		return Result{}, err
	}
	return e.ensure(ctx, opts)
}

func (e *env) ensure(ctx context.Context, opts Options) (Result, error) {
	if e.goos != "darwin" {
		return Result{}, fmt.Errorf("lezz.sea is a macOS application; this host is %s", e.goos)
	}

	if path, ok := Installed(e.home); ok && !opts.Force {
		e.logf("using %s", path)
		info, vErr := e.inspect(ctx, path, opts.AllowUnnotarized)
		if vErr != nil {
			return Result{}, fmt.Errorf("the installed %s does not pass verification: %w", AppName, vErr)
		}
		return Result{Path: path, Version: info.Version, Build: info.Build, Notarized: info.Notarized}, nil
	}

	e.logf("looking for a %s build of %s", e.channel, Slug)
	m, err := e.fetchManifest(ctx)
	if err != nil {
		return Result{}, err
	}
	e.logf("published %s (build %s, %s)", m.Release, m.Build, m.Commit[:min(12, len(m.Commit))])

	archive, cleanArchive, err := e.fetchArchive(ctx, m)
	if err != nil {
		return Result{}, err
	}
	defer cleanArchive()

	work, err := os.MkdirTemp("", "lezz-sea-")
	if err != nil {
		return Result{}, fmt.Errorf("create work dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	app, err := e.extract(ctx, archive, work)
	if err != nil {
		return Result{}, err
	}

	e.logf("verifying the bundle")
	info, err := e.inspect(ctx, app, opts.AllowUnnotarized)
	if err != nil {
		return Result{}, err
	}
	if m.Build != "" && info.Build != m.Build {
		return Result{}, fmt.Errorf("the manifest says build %s but the bundle is build %q — the archive and the manifest disagree", m.Build, info.Build)
	}

	dest, err := e.install(ctx, app)
	if err != nil {
		return Result{}, err
	}
	e.logf("verified %s, team %s, notarized=%t", info.Authority, info.TeamID, info.Notarized)
	return Result{
		Path:       dest,
		Version:    info.Version,
		Build:      info.Build,
		Notarized:  info.Notarized,
		Downloaded: true,
	}, nil
}

// Installed reports the path of an installed dashboard, checking the per-user
// and system application directories in that order.
func Installed(home string) (string, bool) {
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", false
		}
	}
	for _, dir := range []string{filepath.Join(home, "Applications"), "/Applications"} {
		path := filepath.Join(dir, AppName)
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			return path, true
		}
	}
	return "", false
}

// Inspect verifies an existing bundle, applying the same checks an install
// would. It performs no network access.
func Inspect(ctx context.Context, appPath string, allowUnnotarized bool) (Info, error) {
	e, err := newEnv(Options{})
	if err != nil {
		return Info{}, err
	}
	return e.inspect(ctx, appPath, allowUnnotarized)
}
