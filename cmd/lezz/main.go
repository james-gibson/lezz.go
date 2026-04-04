package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/james-gibson/lezz.go/internal/selfupdate"
	"github.com/james-gibson/lezz.go/internal/tools"
)

const version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(0)
	}

	ctx := context.Background()

	switch os.Args[1] {
	case "run":
		cmdRun()

	case "install":
		cmdInstall(ctx)

	case "update":
		cmdUpdate(ctx)

	case "service":
		cmdService()

	case "version":
		cmdVersion()

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
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
	fmt.Printf("lezz %s — checking for updates...\n", version)

	latest, hasUpdate, err := selfupdate.Check(ctx, version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update check failed:", err)
		os.Exit(1)
	}

	if !hasUpdate {
		fmt.Printf("already up to date (%s)\n", version)
		return
	}

	fmt.Printf("new version available: %s → applying...\n", latest)
	applied, err := selfupdate.Apply(ctx, version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update failed:", err)
		os.Exit(1)
	}
	fmt.Printf("updated to %s — restart lezz to use the new version\n", applied)
}

// lezz service install|remove <tool>
func cmdService() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: lezz service install|remove <tool>")
		os.Exit(1)
	}
	action := os.Args[2]
	name := os.Args[3]

	switch action {
	case "install":
		fmt.Fprintf(os.Stderr, "service install for %q: not yet implemented\n", name)
		os.Exit(1)
	case "remove":
		fmt.Fprintf(os.Stderr, "service remove for %q: not yet implemented\n", name)
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown service action %q; use install or remove\n", action)
		os.Exit(1)
	}
}

func cmdVersion() {
	fmt.Println("lezz", version)
	fmt.Println()

	binDir, _ := tools.BinDir()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "TOOL\tVERSION\tDAEMON")
	for _, t := range tools.Registry {
		var toolVersion string
		switch {
		case t.Name == "lezz":
			toolVersion = version
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
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name, toolVersion, "not configured")
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
  lezz run <tool> [args...]          Launch a managed tool
  lezz install <tool>                Download and install a managed tool
  lezz update                        Check for and apply lezz self-update
  lezz service install <tool>        Configure systemd/cron for a tool
  lezz service remove <tool>         Remove daemon config for a tool
  lezz version                       Print version

Managed tools: lezz, adhd, ocd-smoke-alarm, tuner
`)
}
