package demo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/james-gibson/lezz.go/internal/seed"
)

// ledgerPath gives each test its own ledger, the way ~/.lezz has its own.
func ledgerPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "lab", "projects", "routes.json")
}

func TestProjectRoutesMintsOnce(t *testing.T) {
	path := ledgerPath(t)

	first, err := newProjectRoutes(path)
	if err != nil {
		t.Fatal(err)
	}
	route, err := first.routeFor("grep", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if route == "" {
		t.Fatal("minted an empty route")
	}

	// A restart reads the ledger rather than minting: a new route per launch
	// renames the project, which is the failure this exists to prevent.
	second, err := newProjectRoutes(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := second.routeFor("grep", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if again != route {
		t.Fatalf("route changed across a restart: %q -> %q", route, again)
	}

	// Distinct projects get distinct routes.
	other, err := second.routeFor("lemon-extract", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if other == route {
		t.Fatalf("two projects share the route %q", route)
	}
}

func TestProjectRoutesExplicitOverrideWinsAndIsNotPersisted(t *testing.T) {
	path := ledgerPath(t)
	routes, err := newProjectRoutes(path)
	if err != nil {
		t.Fatal(err)
	}

	route, err := routes.routeFor("grep", "", "20261019T210927-09001816")
	if err != nil {
		t.Fatal(err)
	}
	if route != "20261019T210927-09001816" {
		t.Fatalf("explicit route ignored: %q", route)
	}

	if _, ok := routes.routes["grep"]; ok {
		t.Fatal("an operator-supplied route must not be recorded as lezz's own")
	}
}

func TestProjectRoutesReadsSeedFileInCwd(t *testing.T) {
	dir := t.TempDir()
	f, err := seed.New("grep", "base")
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := f.Write(filepath.Join(dir, "seed.json")); writeErr != nil {
		t.Fatal(writeErr)
	}

	routes, err := newProjectRoutes(ledgerPath(t))
	if err != nil {
		t.Fatal(err)
	}
	route, err := routes.routeFor("grep", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if route != f.Seed.ID {
		t.Fatalf("route = %q, want the project's own %q", route, f.Seed.ID)
	}
}

func TestProjectRoutesRejectsUnreadableSeedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seed.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	routes, err := newProjectRoutes(ledgerPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routes.routeFor("grep", dir, ""); err == nil {
		t.Fatal("expected a broken seed.json to fail rather than mint a second route")
	}
}

func TestProjectRoutesToleratesMissingLedger(t *testing.T) {
	routes, err := newProjectRoutes(ledgerPath(t))
	if err != nil {
		t.Fatalf("a missing ledger is an empty ledger, got %v", err)
	}
	if len(routes.routes) != 0 {
		t.Fatalf("expected no routes, got %v", routes.routes)
	}
}

func TestProjectRoutesRejectsCorruptLedger(t *testing.T) {
	path := ledgerPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[this is not a map]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newProjectRoutes(path); err == nil {
		t.Fatal("expected a corrupt ledger to be an error, not a silent reset")
	}
}

// TestClusterJoinParamsCarriesRoute covers the wire contract adhd reads
// (mcpserver.projectRoute prefers "route", then "id").
func TestClusterJoinParamsCarriesRoute(t *testing.T) {
	data := clusterJoinParams(ClusterInfo{
		Name: "cluster",
		Projects: []ProjectEntry{
			{Route: "20261019T210927-09001816", ID: "20261019T210927-09001816", Name: "grep", MCPURL: "http://127.0.0.1:1/mcp"},
			{Name: "legacy", MCPURL: "http://127.0.0.1:2/mcp"},
		},
	})
	s := string(data)
	if !strings.Contains(s, `"route":"20261019T210927-09001816"`) {
		t.Fatalf("project route missing: %s", s)
	}
	if strings.Count(s, `"route":`) != 1 {
		t.Fatalf("a project without a route must not send one: %s", s)
	}
}
