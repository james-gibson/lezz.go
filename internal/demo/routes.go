package demo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/james-gibson/lezz.go/internal/seed"
)

// projectRouteEnv is how lezz tells a child process which project it is. It is
// the name adhd and workspace-mcp already read (docs/sea-hub.md), so a child
// that publishes its own project does not need to know about lezz's ledger.
const projectRouteEnv = "LEZZ_PROJECT_ROUTE"

// projectRoutesFile is where lezz remembers the routes it minted for the
// projects it launches: ~/.lezz/lab/projects/routes.json, next to the bridge
// overrides it belongs with.
func projectRoutesFile() (string, error) {
	dir, err := lezzDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lab", "projects", "routes.json"), nil
}

// projectRoutes remembers the routing id lezz minted for each project it
// launches.
//
// A route is what a dependent resolves a project by (ADR 0003): it survives a
// cluster restart and a port rebind, which the instance name and the MCP URL do
// not. A project that declares its own route keeps it; only a project with
// nothing to read gets one minted here, and the minted value is written down so
// the next start re-announces the same project instead of renaming it.
type projectRoutes struct {
	path   string
	routes map[string]string
}

// newProjectRoutes loads the ledger at path, treating a missing file as empty.
// A ledger that exists but cannot be parsed is an error: silently starting from
// empty is how every project gets renamed on the next restart.
func newProjectRoutes(path string) (*projectRoutes, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create project route dir: %w", err)
	}

	r := &projectRoutes{path: path, routes: map[string]string{}}
	data, err := os.ReadFile(path) //nolint:gosec // path is lezz's own state file under ~/.lezz
	switch {
	case err == nil:
		if unmarshalErr := json.Unmarshal(data, &r.routes); unmarshalErr != nil {
			return nil, fmt.Errorf("parse project routes %s: %w", path, unmarshalErr)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("read project routes %s: %w", path, err)
	}
	return r, nil
}

// routeFor resolves one project's route, in the order that keeps identity
// stable:
//
//  1. an explicit LEZZ_PROJECT_ROUTE in the project's own environment,
//  2. the route its seed.json declares, when it has one,
//  3. the route lezz minted for it last time,
//  4. a freshly minted route, which is written down before it is returned.
//
// A seed.json that exists but does not load is an error rather than a reason to
// mint a second route: the project already has an identity, and quietly
// replacing it is the rename this whole mechanism exists to prevent.
func (r *projectRoutes) routeFor(name, cwd, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	if cwd != "" {
		route, found, err := seedRoute(filepath.Join(cwd, "seed.json"))
		if err != nil {
			return "", err
		}
		if found {
			return route, nil
		}
	}

	if existing := r.routes[name]; existing != "" {
		return existing, nil
	}

	route, err := seed.MintRoute()
	if err != nil {
		return "", fmt.Errorf("mint route for %s: %w", name, err)
	}
	r.routes[name] = route
	if err := r.save(); err != nil {
		return "", err
	}
	return route, nil
}

// seedRoute reads the route a project declares, if it declares one at all.
func seedRoute(path string) (string, bool, error) {
	f, err := seed.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read project route from %s: %w", path, err)
	}
	return f.Route(), true, nil
}

// save writes the ledger atomically, so a crash mid-write cannot leave every
// project in it unreadable.
func (r *projectRoutes) save() error {
	data, err := json.MarshalIndent(r.routes, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project routes: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".routes-*")
	if err != nil {
		return fmt.Errorf("create project route temp file: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("write project routes: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close project routes: %w", err)
	}
	if err := os.Rename(name, r.path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("replace project routes: %w", err)
	}
	return nil
}
