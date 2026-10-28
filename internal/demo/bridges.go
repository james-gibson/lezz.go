package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/james-gibson/isotope"
	"github.com/james-gibson/lezz.go/internal/bridge"
	"github.com/james-gibson/lezz.go/internal/credential"
)

// runningBridge is one lezz-owned stdio skill exposed over loopback HTTP.
type runningBridge struct {
	cfg   bridge.Config
	b     *bridge.Bridge
	route string // stable project route, exported to the child and the cluster
}

// bridgeOverrideDir is where an operator can add or replace lezz's embedded
// bridge definitions without rebuilding.
func bridgeOverrideDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".lezz", "lab", "bridges")
}

// startBridges starts every configured stdio skill. lezz owns both the static
// config (embedded + operator overlay) and the child process; adhd and
// smoke-alarm only ever see the resulting HTTP endpoint.
//
// Each child is told its project route through LEZZ_PROJECT_ROUTE, and the same
// route is carried on the runningBridge so the cluster's project entry names a
// project a dependent can resolve after a restart (lezz.go#62).
func startBridges(ctx context.Context, logDir string, routes *projectRoutes) ([]*runningBridge, error) {
	cfgs, err := bridge.LoadConfigs(bridgeOverrideDir())
	if err != nil {
		return nil, err
	}
	return startBridgesFrom(ctx, logDir, routes, cfgs)
}

// startBridgesFrom is startBridges with the config list supplied. The demo loads
// it from the embedded defaults plus the operator overlay; the acceptance test
// supplies one bridge, so what it asserts does not depend on what else is
// installed on the machine.
func startBridgesFrom(ctx context.Context, logDir string, routes *projectRoutes, cfgs []bridge.Config) ([]*runningBridge, error) {
	out := make([]*runningBridge, 0, len(cfgs))
	for i := range cfgs {
		cfg := &cfgs[i]
		route, routeErr := routes.routeFor(cfg.Name, cfg.Cwd, cfg.Env[projectRouteEnv])
		if routeErr != nil {
			closeBridges(out)
			return nil, fmt.Errorf("route for skill %s: %w", cfg.Name, routeErr)
		}
		cfg.Env = withEnv(cfg.Env, projectRouteEnv, route)

		br, startErr := bridge.Start(ctx, *cfg, bridge.Options{
			LogPath:       filepath.Join(logDir, "bridge-"+cfg.Name+".log"),
			ResolveSecret: credential.Resolve,
		})
		if startErr != nil {
			closeBridges(out)
			return nil, fmt.Errorf("start bridge %s: %w", cfg.Name, startErr)
		}
		out = append(out, &runningBridge{cfg: *cfg, b: br, route: route})
		fmt.Printf("skill %-10s %s  (%s %s)\n", cfg.Name, br.URL(), cfg.Command, strings.Join(cfg.Args, " "))
	}
	return out, nil
}

// withEnv returns env with key set to value, copying rather than mutating the
// map a caller may still hold. An operator's own setting is not overwritten
// here: routeFor already resolved that value as the route.
func withEnv(env map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(env)+1)
	for k, v := range env {
		out[k] = v
	}
	out[key] = value
	return out
}

func closeBridges(bridges []*runningBridge) {
	for _, br := range bridges {
		_ = br.b.Close()
	}
}

// bridgeProjects turns running bridges into cluster project entries, so the
// existing adhd.cluster.join path mints adhd.project.<route>.call without adhd
// learning anything about stdio.
//
// Route names the project across restarts; ID carries the same value as the
// legacy alias, so an adhd that predates project.route namespaces the tool the
// same way the new one does.
func bridgeProjects(bridges []*runningBridge) []ProjectEntry {
	out := make([]ProjectEntry, 0, len(bridges))
	for _, br := range bridges {
		out = append(out, ProjectEntry{
			Route:  br.route,
			ID:     br.route,
			Name:   br.cfg.Name,
			Repo:   br.cfg.Repo,
			MCPURL: br.b.URL(),
		})
	}
	return out
}

// registerBridges announces each skill to the smoke-alarm isotope registry.
// The alarm assigns a rung (rung 2 here: self-registered, not an adhd_mcp in
// the cluster registry) and lists it on /isotope/list, which is the lab's
// discovery surface.
func registerBridges(ctx context.Context, bridges []*runningBridge, alarms ...string) {
	for _, br := range bridges {
		for _, alarm := range alarms {
			client := isotope.NewClient(alarm)
			regCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			rec, err := client.Register(regCtx, isotope.Registration{
				Name:     br.cfg.Name,
				Role:     "skill",
				Endpoint: br.b.URL(),
				Protocol: "mcp",
			})
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: register skill %s with %s: %v\n", br.cfg.Name, alarm, err)
				continue
			}
			fmt.Printf("registered skill %-10s with %s  rung %d (%s)\n",
				rec.Name, alarm, rec.TrustRung, rec.RungName)
		}
	}
}
