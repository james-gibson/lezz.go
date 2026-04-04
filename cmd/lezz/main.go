package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"text/tabwriter"

	"github.com/james-gibson/lezz.go/internal/demo"
	"github.com/james-gibson/lezz.go/internal/selfupdate"
	"github.com/james-gibson/lezz.go/internal/service"
	"github.com/james-gibson/lezz.go/internal/tools"
)

// buildVersion is set at build time by goreleaser via
// -ldflags "-X main.buildVersion={{.Version}}".
var buildVersion string

func version() string {
	if buildVersion != "" {
		return buildVersion
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	v := bi.Main.Version
	if v == "" || v == "(devel)" {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 8 {
				return s.Value[:8]
			}
		}
		return "dev"
	}
	return v
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(0)
	}

	ctx, cancel := context.WithCancel(context.Background())

	switch os.Args[1] {
	case "demo":
		cmdDemo(ctx)
		cancel()

	case "run":
		cancel()
		cmdRun()

	case "install":
		cmdInstall(ctx)
		cancel()

	case "update":
		cmdUpdate(ctx)
		cancel()

	case "service":
		cmdService()

	case "start":
		cmdStart()

	case "purge":
		cmdPurge(ctx)
		cancel()

	case "version":
		cmdVersion()

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

// lezz demo
// Launches a self-contained demo cluster: 2 ocd-smoke-alarm instances + adhd headless.
func cmdDemo(ctx context.Context) {
	if err := demo.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// lezz run <tool> [args...]
// Replaces the lezz process with the tool — lezz does not appear in the process tree.
func cmdRun() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lezz run <tool> [args...]")
		fmt.Fprintf(os.Stderr, "managed tools: %s\n", strings.Join(tools.Names(), ", "))
		os.Exit(1)
	}
	name := os.Args[2]
	args := os.Args[3:]

	if err := tools.Run(name, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// lezz install <tool>
func cmdInstall(ctx context.Context) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lezz install <tool>")
		fmt.Fprintf(os.Stderr, "managed tools: %s\n", strings.Join(tools.Names(), ", "))
		os.Exit(1)
	}
	name := os.Args[2]

	tool, ok := tools.Lookup(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown tool %q\nmanaged tools: %s\n", name, strings.Join(tools.Names(), ", "))
		os.Exit(1)
	}

	fmt.Printf("installing %s from %s ...\n", tool.Name, tool.GithubSlug)
	ver, err := tools.Install(ctx, tool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "install failed:", err)
		os.Exit(1)
	}
	fmt.Printf("installed %s %s\n", tool.Name, ver)

	binDir, _ := tools.BinDir()
	if binDir != "" && !isOnPath(binDir) {
		fmt.Printf("\nAdd lezz's bin directory to your PATH:\n")
		fmt.Printf("  echo 'export PATH=\"%s:$PATH\"' >> ~/.zshrc && source ~/.zshrc\n", binDir)
	}
}

// lezz update
func cmdUpdate(ctx context.Context) {
	fmt.Printf("lezz %s — checking for updates...\n", version())

	latest, hasUpdate, err := selfupdate.Check(ctx, version())
	if err != nil {
		fmt.Fprintln(os.Stderr, "update check failed:", err)
		os.Exit(1)
	}

	if !hasUpdate {
		fmt.Printf("already up to date (%s)\n", version())
		return
	}

	fmt.Printf("new version available: %s → applying...\n", latest)
	applied, err := selfupdate.Apply(ctx, version())
	if err != nil {
		fmt.Fprintln(os.Stderr, "update failed:", err)
		os.Exit(1)
	}
	fmt.Printf("updated to %s — restart lezz to use the new version\n", applied)
}

// lezz start <tool> [args...]
// Spawns the tool as a child process and waits for it to exit.
// Unlike "lezz run", lezz stays alive as the parent (useful for scripting clusters).
func cmdStart() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lezz start <tool> [args...]")
		fmt.Fprintf(os.Stderr, "managed tools: %s\n", strings.Join(tools.Names(), ", "))
		os.Exit(1)
	}
	name := os.Args[2]
	args := os.Args[3:]

	cmd, err := tools.Start(name, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := cmd.Wait(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// lezz service install|remove|list|purge [tool] [profile]
func cmdService() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lezz service install|remove|list|purge [tool] [profile]")
		os.Exit(1)
	}
	action := os.Args[2]

	switch action {
	case "list":
		cmdServiceList()
		return
	case "purge":
		cmdServicePurge()
		return
	}

	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: lezz service install|remove <tool> [profile]")
		os.Exit(1)
	}
	toolName := os.Args[3]
	profileName := "idle"
	if len(os.Args) >= 5 {
		profileName = os.Args[4]
	}

	t, ok := tools.Lookup(toolName)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown tool %q\nmanaged tools: %s\n", toolName, strings.Join(tools.Names(), ", "))
		os.Exit(1)
	}

	p, ok := tools.LookupProfile(t, profileName)
	if !ok {
		fmt.Fprintf(os.Stderr, "tool %q has no profile %q\n", toolName, profileName)
		if len(t.Profiles) == 0 {
			fmt.Fprintf(os.Stderr, "%s has no daemon profiles\n", toolName)
		} else {
			fmt.Fprintf(os.Stderr, "available profiles:")
			for _, pr := range t.Profiles {
				fmt.Fprintf(os.Stderr, "\n  %-12s  %s", pr.Name, pr.Description)
			}
			fmt.Fprintln(os.Stderr)
		}
		os.Exit(1)
	}

	switch action {
	case "install":
		binPath := resolveBinPath(t.Name)
		if binPath == "" {
			fmt.Fprintf(os.Stderr, "%s is not installed — run: lezz install %s\n", t.Name, t.Name)
			os.Exit(1)
		}
		if err := service.Install(t, p, binPath); err != nil {
			fmt.Fprintln(os.Stderr, "service install failed:", err)
			os.Exit(1)
		}
		plistPath, _ := service.PlistPath(t, p)
		fmt.Printf("installed %s (%s)\n", t.Name, p.Name)
		fmt.Printf("plist: %s\n", plistPath)
		fmt.Printf("logs:  ~/.lezz/logs/co.james-gibson.lab.%s.%s.{log,err}\n", t.Name, p.Name)
	case "remove":
		if err := service.Remove(t, p); err != nil {
			fmt.Fprintln(os.Stderr, "service remove failed:", err)
			os.Exit(1)
		}
		fmt.Printf("removed %s (%s)\n", t.Name, p.Name)
	default:
		fmt.Fprintf(os.Stderr, "unknown service action %q; use install or remove\n", action)
		os.Exit(1)
	}
}

// cmdPurge removes all lezz-managed state:
//  1. Stops and removes all launchd services.
//  2. Deletes managed binaries from ~/.lezz/bin.
//  3. Clears Go build-cache entries for managed modules (go clean -cache).
//  4. Removes module download cache entries for managed modules (go clean -modcache equivalent).
//
// This is a full reset — re-run `lezz install <tool>` to reinstall.
func cmdPurge(ctx context.Context) {
	// 1. Stop + remove launchd services.
	services, err := service.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "purge: list services:", err)
	} else if len(services) > 0 {
		fmt.Printf("removing %d service(s)...\n", len(services))
		for _, svc := range services {
			fmt.Printf("  %s\n", svc.Label)
		}
		if purgeErr := service.Purge(); purgeErr != nil {
			fmt.Fprintln(os.Stderr, "purge: service purge:", purgeErr)
		}
	}

	// 2. Remove managed binaries from ~/.lezz/bin.
	fmt.Println("removing managed binaries...")
	result, err := tools.PurgeBins()
	if err != nil {
		fmt.Fprintln(os.Stderr, "purge: remove bins:", err)
		os.Exit(1)
	}
	for _, name := range result.BinsRemoved {
		fmt.Printf("  removed ~/.lezz/bin/%s\n", name)
	}
	for _, name := range result.BinsMissing {
		fmt.Printf("  (not installed: %s)\n", name)
	}

	// 3. Clear Go build-cache entries for managed module packages.
	fmt.Println("clearing Go build cache for managed modules...")
	cacheResult := tools.PurgeGoCache(ctx)
	for _, e := range cacheResult.GoCacheErrors {
		fmt.Fprintln(os.Stderr, "purge: go clean -cache:", e)
	}

	// 4. Remove module download cache entries for managed modules.
	fmt.Println("clearing Go module cache for managed modules...")
	modResult := tools.PurgeModCache(ctx)
	for _, removed := range modResult.BinsRemoved {
		fmt.Printf("  removed %s\n", removed)
	}
	for _, e := range modResult.GoCacheErrors {
		fmt.Fprintln(os.Stderr, "purge: mod cache:", e)
	}

	fmt.Println("done — run `lezz install <tool>` to reinstall")
}

func cmdServiceList() {
	services, err := service.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "service list failed:", err)
		os.Exit(1)
	}
	if len(services) == 0 {
		fmt.Println("no lezz-managed services installed")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "LABEL\tSTATUS\tPLIST")
	for _, svc := range services {
		status := "stopped"
		if svc.Running {
			status = "running"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", svc.Label, status, svc.PlistPath)
	}
	_ = w.Flush()
}

func cmdServicePurge() {
	services, err := service.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "service purge failed:", err)
		os.Exit(1)
	}
	if len(services) == 0 {
		fmt.Println("no lezz-managed services to remove")
		return
	}
	for _, svc := range services {
		fmt.Printf("removing %s ...\n", svc.Label)
	}
	if err := service.Purge(); err != nil {
		fmt.Fprintln(os.Stderr, "service purge failed:", err)
		os.Exit(1)
	}
	fmt.Printf("removed %d service(s)\n", len(services))
}

func cmdVersion() {
	fmt.Println("lezz", version())
	fmt.Println()

	binDir, _ := tools.BinDir()

	// Build tool → "profile (status)" strings from installed services.
	daemonStatus := map[string][]string{}
	if svcs, err := service.List(); err == nil {
		for _, svc := range svcs {
			tool, profile, ok := service.ParseLabel(svc.Label)
			if !ok {
				continue
			}
			status := "stopped"
			if svc.Running {
				status = "running"
			}
			daemonStatus[tool] = append(daemonStatus[tool], profile+" ("+status+")")
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "TOOL\tVERSION\tDAEMON")
	for _, t := range tools.Registry {
		var toolVersion string
		switch {
		case t.Name == "lezz":
			toolVersion = version()
		case binDir != "":
			binPath := filepath.Join(binDir, t.Name)
			if _, err := os.Stat(binPath); err == nil {
				toolVersion = toolVersionStr(binPath)
			} else {
				toolVersion = "not installed"
			}
		default:
			toolVersion = "not installed"
		}
		daemon := "not configured"
		if entries := daemonStatus[t.Name]; len(entries) > 0 {
			daemon = strings.Join(entries, ", ")
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name, toolVersion, daemon)
	}
	_ = w.Flush()
}

// toolVersionStr runs <binary> -v and returns the first line of output,
// falling back to "unknown" if the call fails or produces no output.
func toolVersionStr(binPath string) string {
	var out bytes.Buffer
	cmd := exec.Command(binPath, "-v")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "unknown"
	}
	line := strings.TrimSpace(out.String())
	if line == "" {
		return "unknown"
	}
	return line
}

// resolveBinPath returns the absolute path to a managed tool binary.
// For "lezz" itself it falls back to os.Executable() so the running binary
// can be daemonised even before it has been installed into ~/.lezz/bin.
func resolveBinPath(name string) string {
	if binDir, err := tools.BinDir(); err == nil {
		p := filepath.Join(binDir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if name == "lezz" {
		if p, err := os.Executable(); err == nil {
			return p
		}
	}
	return ""
}

func isOnPath(dir string) bool {
	for _, p := range strings.Split(os.Getenv("PATH"), ":") {
		if p == dir {
			return true
		}
	}
	return false
}

func usage() {
	fmt.Print(`lezz — self-updating host for adhd, ocd-smoke-alarm, and tuner

Usage:
  lezz demo                          Launch a self-contained demo cluster
  lezz run <tool> [args...]          Replace lezz with the tool (exec)
  lezz start <tool> [args...]        Spawn the tool as a child process (wait)
  lezz install <tool>                Download and install a managed tool
  lezz purge                         Remove services, binaries, and Go caches for all managed tools
  lezz update                        Check for and apply lezz self-update
  lezz service install <tool>        Configure launchd service for a tool
  lezz service remove <tool>         Remove daemon config for a tool
  lezz service list                  List all installed lezz services
  lezz service purge                 Unload and remove all lezz services
  lezz version                       Print version

Managed tools: lezz, adhd, ocd-smoke-alarm, tuner
`)
}
