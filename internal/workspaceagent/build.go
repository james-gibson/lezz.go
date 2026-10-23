// Package workspaceagent fetches and builds the lab-safety workspace agent.
//
// This is the interim path until workspace-agent is a registered managed tool
// with release assets. The source is the lab-safety repository; the build runs
// the normal Go toolchain in its lab/agent module.
package workspaceagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/james-gibson/lezz.go/internal/tools"
)

// RepoURL is the source repository for workspace-agent.
const RepoURL = "https://github.com/james-gibson/lab-safety.git"

// Options controls a fetch/build.
type Options struct {
	Source  string    // lab-safety checkout, lab/agent module, or agent module; empty = cache
	Out     string    // output path; empty = ~/.lezz/bin/workspace-agent
	NoFetch bool      // use an existing source checkout without git fetch
	Force   bool      // rebuild even if Out exists
	Stdout  io.Writer // progress; default os.Stderr
	Stderr  io.Writer // build/git errors; default os.Stderr
}

// Ensure returns the built binary, building it if it is missing or Force is set.
func Ensure(ctx context.Context, opts Options) (string, error) {
	out, err := outputPath(opts.Out)
	if err != nil {
		return "", err
	}
	if !opts.Force {
		if info, err := os.Stat(out); err == nil && !info.IsDir() {
			return out, nil
		}
	}
	return Build(ctx, opts)
}

// Build fetches (unless Source is set or NoFetch is true) and compiles
// workspace-agent, returning the binary path.
func Build(ctx context.Context, opts Options) (string, error) {
	out, err := outputPath(opts.Out)
	if err != nil {
		return "", err
	}
	source := opts.Source
	if source == "" {
		if local := DiscoverSource(); local != "" {
			source = local
			logf(opts.Stderr, "workspace-agent: using local source %s", source)
		} else {
			source, err = DefaultSourceDir()
			if err != nil {
				return "", err
			}
			if !opts.NoFetch {
				if syncErr := syncRepo(ctx, source, opts.Stderr); syncErr != nil {
					// A cached checkout is still usable offline; only fail when there
					// is nothing to build from.
					if _, statErr := os.Stat(source); statErr != nil {
						return "", syncErr
					}
					logf(opts.Stderr, "workspace-agent: source refresh failed, using existing checkout: %v", syncErr)
				}
			} else if _, statErr := os.Stat(source); statErr != nil {
				return "", fmt.Errorf("workspace-agent source %s is missing and --no-fetch was set", source)
			}
		}
	}

	module, err := findModule(source)
	if err != nil {
		if opts.Source == "" && DiscoverSource() == "" {
			return "", fmt.Errorf("%w\n  the workspace-agent source may not be committed to %s yet; pass --src <local lab-safety checkout>", err, RepoURL)
		}
		return "", err
	}
	if _, lookErr := exec.LookPath("go"); lookErr != nil {
		return "", errors.New("workspace-agent build requires the go toolchain on PATH")
	}
	if mkErr := os.MkdirAll(filepath.Dir(out), 0o750); mkErr != nil {
		return "", fmt.Errorf("create bin dir: %w", mkErr)
	}

	logf(opts.Stderr, "workspace-agent: building from %s", module)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/workspace-agent") //nolint:gosec // out is a resolved path under the managed bin dir
	cmd.Dir = module
	cmd.Stdout = writer(opts.Stdout)
	cmd.Stderr = writer(opts.Stderr)
	if runErr := cmd.Run(); runErr != nil {
		return "", fmt.Errorf("build workspace-agent in %s: %w", module, runErr)
	}
	info, err := os.Stat(out)
	if err != nil {
		return "", fmt.Errorf("workspace-agent build produced no binary: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("workspace-agent output %s is a directory", out)
	}
	logf(opts.Stderr, "workspace-agent: installed %s", out)
	return out, nil
}

// DefaultSourceDir is ~/.lezz/src/lab-safety.
func DefaultSourceDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".lezz", "src", "lab-safety"), nil
}

// DiscoverSource looks for an existing lab-safety checkout before cloning one.
// The cache is deliberately not in this list: Build owns it and may update it.
func DiscoverSource() string {
	if v := strings.TrimSpace(os.Getenv("LEZZ_WORKSPACE_AGENT_SRC")); v != "" {
		if _, err := findModule(v); err == nil {
			return v
		}
	}
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	var candidates []string
	for i := 0; i < 3 && cwd != ""; i++ {
		candidates = append(candidates,
			filepath.Join(cwd, "lab-safety"),
			filepath.Join(cwd, "..", "lab-safety"),
		)
		parent := filepath.Dir(cwd)
		if parent == cwd {
			break
		}
		cwd = parent
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, "projects", "personal", "lab-safety"),
			filepath.Join(home, "projects", "lab-safety"),
			filepath.Join(home, "src", "lab-safety"),
			filepath.Join(home, "lab-safety"),
		)
	}
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		c = filepath.Clean(c)
		if seen[c] {
			continue
		}
		seen[c] = true
		if _, err := findModule(c); err == nil {
			return c
		}
	}
	return ""
}

// DefaultOut is ~/.lezz/bin/workspace-agent.
func DefaultOut() (string, error) {
	bin, err := tools.BinDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(bin, "workspace-agent"), nil
}

func outputPath(out string) (string, error) {
	if out != "" {
		return out, nil
	}
	return DefaultOut()
}

// syncRepo clones or updates the managed source cache. It only ever touches the
// cache directory; a local checkout is passed through Options.Source instead.
func syncRepo(ctx context.Context, dir string, stderr io.Writer) error {
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("workspace-agent source fetch requires git on PATH")
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
			return fmt.Errorf("create source dir: %w", err)
		}
		logf(stderr, "workspace-agent: cloning %s", RepoURL)
		cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", RepoURL, dir) //nolint:gosec // dir is the managed source cache, not caller input
		cmd.Stdout = writer(stderr)
		cmd.Stderr = writer(stderr)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("clone workspace-agent source: %w", err)
		}
		return nil
	}
	logf(stderr, "workspace-agent: updating %s", dir)
	fetch := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--depth", "1", "origin") //nolint:gosec // dir is the managed source cache
	fetch.Stdout = writer(stderr)
	fetch.Stderr = writer(stderr)
	if err := fetch.Run(); err != nil {
		return fmt.Errorf("fetch workspace-agent source: %w", err)
	}
	reset := exec.CommandContext(ctx, "git", "-C", dir, "reset", "--hard", "FETCH_HEAD") //nolint:gosec // dir is the managed source cache
	reset.Stdout = writer(stderr)
	reset.Stderr = writer(stderr)
	if err := reset.Run(); err != nil {
		return fmt.Errorf("update workspace-agent source: %w", err)
	}
	return nil
}

func findModule(source string) (string, error) {
	candidates := []string{
		filepath.Join(source, "lab", "agent"),
		filepath.Join(source, "agent"),
		source,
	}
	for _, dir := range candidates {
		if hasModule(dir) {
			return dir, nil
		}
	}
	return "", fmt.Errorf("workspace-agent source not found under %s (expected lab/agent or an agent module with cmd/workspace-agent)", source)
}

func hasModule(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil { //nolint:gosec // read-only probe of a user-supplied source dir
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "cmd", "workspace-agent")) //nolint:gosec // read-only probe of a user-supplied source dir
	return err == nil && info.IsDir()
}

func writer(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return os.Stderr
}

func logf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(writer(w), format+"\n", args...)
}

// SourceRevision returns the short git revision of a source checkout, when it
// is a git repository. It is used for diagnostics; failure is not fatal.
func SourceRevision(source string) string {
	if source == "" {
		source = DiscoverSource()
		if source == "" {
			source, _ = DefaultSourceDir()
		}
	}
	module, err := findModule(source)
	if err != nil {
		return ""
	}
	repo := module
	for {
		if _, statErr := os.Stat(filepath.Join(repo, ".git")); statErr == nil {
			break
		}
		parent := filepath.Dir(repo)
		if parent == repo {
			return ""
		}
		repo = parent
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--short", "HEAD").Output() //nolint:gosec // repo is the source checkout found above
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
