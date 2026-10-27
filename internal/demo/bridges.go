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
	cfg bridge.Config
	b   *bridge.Bridge
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
func startBridges(ctx context.Context, logDir string) ([]*runningBridge, error) {
	cfgs, err := bridge.LoadConfigs(bridgeOverrideDir())
	if err != nil {
		return nil, err
	}
	out := make([]*runningBridge, 0, len(cfgs))
	for i := range cfgs {
		cfg := &cfgs[i]
		br, startErr := bridge.Start(ctx, *cfg, bridge.Options{
			LogPath:       filepath.Join(logDir, "bridge-"+cfg.Name+".log"),
			ResolveSecret: credential.Resolve,
		})
		if startErr != nil {
			closeBridges(out)
			return nil, fmt.Errorf("start bridge %s: %w", cfg.Name, startErr)
		}
		out = append(out, &runningBridge{cfg: *cfg, b: br})
		fmt.Printf("skill %-10s %s  (%s %s)\n", cfg.Name, br.URL(), cfg.Command, strings.Join(cfg.Args, " "))
	}
	return out, nil
}

func closeBridges(bridges []*runningBridge) {
	for _, br := range bridges {
		_ = br.b.Close()
	}
}

// bridgeProjects turns running bridges into cluster project entries, so the
// existing adhd.cluster.join path mints adhd.project.<name>.call without adhd
// learning anything about stdio.
func bridgeProjects(bridges []*runningBridge) []ProjectEntry {
	out := make([]ProjectEntry, 0, len(bridges))
	for _, br := range bridges {
		out = append(out, ProjectEntry{
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
