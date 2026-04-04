package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PurgeResult summarises what was removed during a purge.
type PurgeResult struct {
	BinsRemoved   []string // tool names whose binaries were deleted from ~/.lezz/bin
	BinsMissing   []string // tool names not present in ~/.lezz/bin (skipped)
	GoCacheErrors []error  // non-fatal errors from go clean -cache
}

// PurgeBins removes the managed tool binaries from ~/.lezz/bin.
// Missing binaries are noted in BinsMissing but are not errors.
func PurgeBins() (PurgeResult, error) {
	binDir, err := BinDir()
	if err != nil {
		return PurgeResult{}, fmt.Errorf("resolve bin dir: %w", err)
	}

	var r PurgeResult
	for _, t := range Registry {
		p := filepath.Join(binDir, t.Name)
		if err := os.Remove(p); err != nil {
			if os.IsNotExist(err) {
				r.BinsMissing = append(r.BinsMissing, t.Name)
			} else {
				return r, fmt.Errorf("remove %s: %w", p, err)
			}
		} else {
			r.BinsRemoved = append(r.BinsRemoved, t.Name)
		}
	}
	return r, nil
}

// PurgeGoCache runs `go clean -cache <module>/...` for each managed tool's
// module, removing build cache entries for those packages. Non-fatal: errors
// are collected in GoCacheErrors rather than aborting.
//
// This does not touch the module download cache (go clean -modcache); that
// is a heavier operation the caller can opt into separately.
func PurgeGoCache(ctx context.Context) PurgeResult {
	var r PurgeResult
	for _, t := range Registry {
		if t.GithubSlug == "" {
			continue
		}
		// Module import path: github.com/<slug>/...
		// GithubSlug is "owner/repo" so module root is github.com/owner/repo.
		pkg := "github.com/" + t.GithubSlug + "/..."
		cmd := exec.CommandContext(ctx, "go", "clean", "-cache", pkg) //nolint:gosec // pkg is constructed from the registry slug
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			r.GoCacheErrors = append(r.GoCacheErrors, fmt.Errorf("go clean -cache %s: %w", pkg, err))
		}
	}
	return r
}

// PurgeModCache removes module download cache entries for all managed tools.
// Each managed module's directory under GOMODCACHE is deleted.
// Non-fatal: errors are collected rather than aborting.
func PurgeModCache(ctx context.Context) PurgeResult {
	var r PurgeResult

	gopathOut, err := exec.CommandContext(ctx, "go", "env", "GOMODCACHE").Output()
	if err != nil {
		r.GoCacheErrors = append(r.GoCacheErrors, fmt.Errorf("go env GOMODCACHE: %w", err))
		return r
	}
	modCache := strings.TrimSpace(string(gopathOut))
	if modCache == "" {
		return r
	}

	for _, t := range Registry {
		if t.GithubSlug == "" {
			continue
		}
		// Module cache layout: $GOMODCACHE/github.com/<owner>/<repo>@<version>
		// Glob for any version of this module.
		pattern := filepath.Join(modCache, "github.com", t.GithubSlug+"@*")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			r.GoCacheErrors = append(r.GoCacheErrors, fmt.Errorf("glob %s: %w", pattern, err))
			continue
		}
		for _, m := range matches {
			// The module cache is read-only by design; chmod before removing.
			if err := chmodRW(m); err != nil {
				r.GoCacheErrors = append(r.GoCacheErrors, fmt.Errorf("chmod %s: %w", m, err))
				continue
			}
			if err := os.RemoveAll(m); err != nil {
				r.GoCacheErrors = append(r.GoCacheErrors, fmt.Errorf("remove %s: %w", m, err))
			} else {
				r.BinsRemoved = append(r.BinsRemoved, m)
			}
		}
	}
	return r
}

// chmodRW recursively makes a directory tree writable so os.RemoveAll can
// delete the read-only files that Go places in the module cache.
func chmodRW(path string) error {
	return filepath.Walk(path, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chmod(p, 0o755) //nolint:gosec // explicit permission grant needed to delete Go module cache
	})
}
