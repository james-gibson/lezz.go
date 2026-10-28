package demo

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/james-gibson/lezz.go/internal/bridge"
)

// TestStartBridgesExportsAndAdvertisesTheProjectRoute is the acceptance test for
// lezz.go#62: a project the demo starts has a route, the route reaches the child
// as LEZZ_PROJECT_ROUTE, the cluster entry carries it, and a restart reuses it
// instead of minting a new one.
func TestStartBridgesExportsAndAdvertisesTheProjectRoute(t *testing.T) {
	dumpPath := filepath.Join(t.TempDir(), "route-dump.txt")
	ledger := filepath.Join(t.TempDir(), "lab", "projects", "routes.json")

	routes, err := newProjectRoutes(ledger)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}

	bridges, err := startBridgesFrom(context.Background(), t.TempDir(), routes, []bridge.Config{{
		Name:         "routetest",
		Repo:         "james-gibson/routetest",
		Command:      os.Args[0],
		Args:         []string{"-test.run=TestBridgeRouteChildProcess"},
		Env:          map[string]string{"LEZZ_TEST_CHILD": "1", "LEZZ_TEST_ROUTE_DUMP": dumpPath},
		Listen:       "127.0.0.1:0",
		AllowedTools: []string{"echo"},
	}})
	if err != nil {
		t.Fatalf("startBridgesFrom: %v", err)
	}
	defer closeBridges(bridges)

	entries := bridgeProjects(bridges)
	if len(entries) != 1 {
		t.Fatalf("expected one project entry, got %+v", entries)
	}
	route := entries[0].Route
	if route == "" {
		t.Fatal("the launched project has no route")
	}
	if entries[0].ID != route {
		t.Fatalf("legacy id %q should alias the route %q", entries[0].ID, route)
	}
	if got := routes.routes["routetest"]; got != route {
		t.Fatalf("ledger holds %q, project advertises %q", got, route)
	}

	// The child is told which project it is, so it can publish itself without
	// reading lezz's ledger.
	dump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("child did not record its environment: %v", err)
	}
	if got := string(dump); got != route {
		t.Fatalf("child saw LEZZ_PROJECT_ROUTE=%q, want %q", got, route)
	}

	// A restart reads the ledger: the same project keeps its route.
	restarted, err := newProjectRoutes(ledger)
	if err != nil {
		t.Fatalf("reload ledger: %v", err)
	}
	again, err := restarted.routeFor("routetest", "", "")
	if err != nil {
		t.Fatalf("route after restart: %v", err)
	}
	if again != route {
		t.Fatalf("route changed across a restart: %q -> %q", route, again)
	}
}

// TestBridgeRouteChildProcess is the fake MCP child the test above starts. It
// speaks just enough stdio JSON-RPC for bridge.Start's initialize handshake and
// records the project route it was given before doing so.
func TestBridgeRouteChildProcess(t *testing.T) {
	if os.Getenv("LEZZ_TEST_CHILD") != "1" {
		t.Skip("helper process for TestStartBridgesExportsAndAdvertisesTheProjectRoute")
	}

	if dump := os.Getenv("LEZZ_TEST_ROUTE_DUMP"); dump != "" {
		if err := os.WriteFile(dump, []byte(os.Getenv(projectRouteEnv)), 0o600); err != nil {
			t.Fatalf("record route: %v", err)
		}
	}

	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil || len(req.ID) == 0 {
			continue // a notification, or noise from the test harness
		}
		if err := enc.Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{},
		}); err != nil {
			return
		}
	}
}
