package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/james-gibson/lezz.go/internal/agents"
	"github.com/james-gibson/lezz.go/internal/anon"
	"github.com/james-gibson/lezz.go/internal/appbundle"
	"github.com/james-gibson/lezz.go/internal/cache"
	"github.com/james-gibson/lezz.go/internal/credential"
	"github.com/james-gibson/lezz.go/internal/debuglease"
	"github.com/james-gibson/lezz.go/internal/demo"
	"github.com/james-gibson/lezz.go/internal/githubauth"
	"github.com/james-gibson/lezz.go/internal/mcpwrapper"
	"github.com/james-gibson/lezz.go/internal/queue"
	"github.com/james-gibson/lezz.go/internal/seed"
	"github.com/james-gibson/lezz.go/internal/selfupdate"
	"github.com/james-gibson/lezz.go/internal/service"
	"github.com/james-gibson/lezz.go/internal/tools"
	"github.com/james-gibson/lezz.go/internal/workspaceagent"

	"github.com/james-gibson/isotope"
)

// buildVersion is set at build time by goreleaser via
// -ldflags "-X main.buildVersion={{.Version}}".
var buildVersion string

// buildGitHubClientID is the public OAuth App client ID, set at build time:
// -ldflags "-X main.buildGitHubClientID=<id>". It is not a secret; device flow
// never uses a client secret.
var buildGitHubClientID string

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
	os.Args = normalizeArgs(os.Args)
	if len(os.Args) < 2 {
		usage()
		os.Exit(0)
	}

	ctx, cancel := context.WithCancel(context.Background())

	switch os.Args[1] {
	case "demo":
		cmdDemo(ctx)
		cancel()

	case "auto":
		cmdAuto(ctx)
		cancel()

	case "queue":
		cmdQueue(ctx)
		cancel()

	case "cache":
		cmdCache(ctx)
		cancel()

	case "mcp":
		cmdMCP(ctx)
		cancel()

	case "anon":
		cmdAnon(ctx)
		cancel()

	case "seed":
		cmdSeed()
		cancel()

	case "see":
		cmdSee(ctx)
		cancel()

	case "debug":
		cmdDebug(ctx)
		cancel()

	case "app":
		cmdApp()
		cancel()

	case "auth":
		cmdAuth(ctx)
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

	case "agent":
		cmdAgent(ctx)

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

// lezz auto
// Launches a base cluster in auto mode: installs any missing managed tools,
// reuses an existing discovery registry, and starts the cluster empty and
// awaiting — targets/config arrive via the server's auto-refresh and remote
// update paths instead of a hardcoded config.
func cmdAuto(ctx context.Context) {
	if err := demo.RunAuto(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "auto:", err)
		os.Exit(1)
	}
}

// lezz seed init [--surface base] [--auth-ref env:NAME] [--name NAME] [--force]
// Creates seed.json: a stable project id, the surface to join, and an external
// auth reference. It never writes a secret and never touches a GitHub identity.
func cmdSeed() {
	if len(os.Args) < 3 || os.Args[2] != "init" {
		fmt.Fprintln(os.Stderr, "usage: lezz seed init [--surface base] [--auth-ref env:LEZZ_CLUSTER_KEY] [--name NAME] [--force]")
		os.Exit(1)
	}
	fs := flag.NewFlagSet("seed init", flag.ContinueOnError)
	surface := fs.String("surface", "base", "cluster surface to join or create")
	authRef := fs.String("auth-ref", "env:LEZZ_CLUSTER_KEY", "external auth reference (env:NAME, file:/path, keychain:NAME)")
	name := fs.String("name", "", "project name (default: current folder)")
	force := fs.Bool("force", false, "overwrite an existing seed.json")
	if err := fs.Parse(os.Args[3:]); err != nil {
		os.Exit(1)
	}
	path := "seed.json"
	if *name == "" {
		if cwd, err := os.Getwd(); err == nil {
			*name = filepath.Base(cwd)
		}
	}
	f, err := seed.New(*name, *surface)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	f.Auth.Ref = *authRef
	if !*force {
		if _, err := os.Stat(path); err == nil {
			fmt.Fprintln(os.Stderr, "seed: seed.json already exists (use --force to overwrite)")
			os.Exit(1)
		}
	}
	if err := f.Write(path); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n  seed    %s\n  surface %s\n  auth    %s (reference only; no secret stored)\n", path, f.Seed.ID, f.Seed.Surface, f.Auth.Ref)
}

// lezz debug keygen|lease|fetch-keys
func cmdDebug(ctx context.Context) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lezz debug keygen|lease|fetch-keys")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "keygen":
		fs := flag.NewFlagSet("debug keygen", flag.ContinueOnError)
		dir := fs.String("dir", debuglease.DefaultDir(), "key directory")
		if err := fs.Parse(os.Args[3:]); err != nil {
			os.Exit(1)
		}
		key, allowed, err := debuglease.EnsureDefaultKey(*dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "debug keygen:", err)
			os.Exit(1)
		}
		fmt.Printf("private  %s (0600)\nallowed  %s\n", key, allowed)
	case "lease":
		fs := flag.NewFlagSet("debug lease", flag.ContinueOnError)
		seedID := fs.String("seed", "", "seed id")
		ttl := fs.Duration("ttl", 5*time.Minute, "lease lifetime")
		actions := fs.String("actions", "observe,explain,shadow", "comma-separated actions")
		key := fs.String("key", "", "signing key (default: local debug key)")
		signerID := fs.String("signer", "local", "allowed_signers identity")
		out := fs.String("out", "lease.json", "lease output path")
		if err := fs.Parse(os.Args[3:]); err != nil {
			os.Exit(1)
		}
		if *seedID == "" {
			f, err := seed.Load("seed.json")
			if err != nil {
				fmt.Fprintln(os.Stderr, "debug lease: --seed or seed.json required")
				os.Exit(1)
			}
			*seedID = f.Seed.ID
		}
		if *key == "" {
			k, _, err := debuglease.EnsureDefaultKey(debuglease.DefaultDir())
			if err != nil {
				fmt.Fprintln(os.Stderr, "debug lease:", err)
				os.Exit(1)
			}
			*key = k
		}
		lease := debuglease.New(*seedID, strings.Split(*actions, ","), *ttl, *signerID)
		if err := lease.Write(*out); err != nil {
			fmt.Fprintln(os.Stderr, "debug lease:", err)
			os.Exit(1)
		}
		if err := debuglease.Sign(*out, *key); err != nil {
			fmt.Fprintln(os.Stderr, "debug lease:", err)
			os.Exit(1)
		}
		fmt.Printf("lease    %s\nsig      %s.sig\nseed     %s\nactions  %s\nexpires  %s\n", *out, *out, *seedID, *actions, lease.NotAfter.Format(time.RFC3339))
	case "fetch-keys":
		fs := flag.NewFlagSet("debug fetch-keys", flag.ContinueOnError)
		user := fs.String("user", "", "GitHub username")
		refresh := fs.Bool("refresh", false, "re-fetch and overwrite the pinned file")
		if err := fs.Parse(os.Args[3:]); err != nil {
			os.Exit(1)
		}
		if *user == "" {
			fmt.Fprintln(os.Stderr, "debug fetch-keys: --user is required")
			os.Exit(1)
		}
		path, err := debuglease.FetchAndPin(ctx, debuglease.DefaultDir(), *user, *refresh)
		if err != nil {
			fmt.Fprintln(os.Stderr, "debug fetch-keys:", err)
			os.Exit(1)
		}
		fmt.Printf("allowed  %s\n", path)
	default:
		fmt.Fprintf(os.Stderr, "unknown debug subcommand: %s\n", os.Args[2])
		os.Exit(1)
	}
}

// lezz see [--file seed.json]
// Reads the seed, recomputes the 42i start, resolves the auth reference, and
// shows the surface. It does not join or create anything yet.
func cmdSee(ctx context.Context) {
	fs := flag.NewFlagSet("see", flag.ContinueOnError)
	path := fs.String("file", "seed.json", "path to seed.json")
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(1)
	}
	f, err := seed.Load(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "see: "+err.Error())
		fmt.Fprintln(os.Stderr, "see: run `lezz seed init` in this folder first")
		os.Exit(1)
	}
	pos := isotope.NewAgentState(f.Seed.ID).Position
	authOK, authErr := seed.ResolveAuth(f.Auth.Ref)
	authState := "absent"
	if authOK {
		authState = "present"
	} else if authErr != nil {
		authState = "invalid: " + authErr.Error()
	}
	fmt.Printf("seed     %s\nsurface  %s\n42i      %s\nauth     %s (%s)\n", f.Seed.ID, f.Seed.Surface, pos.String(), authState, f.Auth.Ref)
	if !authOK {
		fmt.Println("         anonymous/empty surface only until an external key is present")
	}

	url := fmt.Sprintf("http://localhost:%d/cluster", demo.DiscoveryPort)
	clusters, err := demo.FetchRegistry(ctx, url)
	if err != nil || len(clusters) == 0 {
		fmt.Printf("registry  none on :%d — surface %q not running\n", demo.DiscoveryPort, f.Seed.Surface)
		fmt.Println("          start it with `lezz auto` (or create it explicitly)")
		return
	}
	matched := 0
	for _, c := range clusters {
		if c.Name == f.Seed.Surface || strings.HasPrefix(c.Name, f.Seed.Surface+"-") {
			matched++
			fmt.Printf("join      %s\n  alarm-a  %s\n  alarm-b  %s\n  adhd     %s\n", c.Name, c.AlarmA, c.AlarmB, c.AdhdMCP)
		}
	}
	if matched == 0 {
		fmt.Printf("registry  %d cluster(s), none matching surface %q\n", len(clusters), f.Seed.Surface)
	}
}

// lezz anon [--listen host:port] [--lease path] [--signer github:user|default]
// Serves the anonymous MCP surface: a valid server with an empty tool list.
// A debug Unix socket opens only with a verified, unexpired lease.
func cmdAnon(ctx context.Context) {
	fs := flag.NewFlagSet("anon", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:0", "MCP listen address")
	name := fs.String("name", "anonymous", "serverInfo name")
	debugSocket := fs.String("debug-socket", "", "debug Unix socket (default ~/.lezz/run/anon-debug.sock)")
	leasePath := fs.String("lease", "", "signed debug lease JSON")
	allowed := fs.String("allowed-signers", "", "allowed_signers file for lease verification")
	signer := fs.String("signer", "", "lease trust source: github:USER or default")
	identity := fs.String("identity", "", "allowed_signers identity (default: github user or local)")
	seedID := fs.String("seed", "", "expected seed id (default: seed.json in the current folder)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(1)
	}

	// Resolve the trust root only when a lease is supplied: the debug flag alone
	// must not create keys or open anything.
	allowedFile := *allowed
	signerID := *identity
	if *leasePath != "" && allowedFile == "" {
		switch {
		case strings.HasPrefix(*signer, "github:"):
			user := strings.TrimPrefix(*signer, "github:")
			path, err := debuglease.FetchAndPin(ctx, debuglease.DefaultDir(), user, false)
			if err != nil {
				fmt.Fprintf(os.Stderr, "anon: github keys unavailable (%v); using local default key\n", err)
				_, path, err = debuglease.EnsureDefaultKey(debuglease.DefaultDir())
				if err != nil {
					fmt.Fprintln(os.Stderr, "anon:", err)
					os.Exit(1)
				}
				signerID = "local"
			} else {
				signerID = user
			}
			allowedFile = path
		default:
			_, path, err := debuglease.EnsureDefaultKey(debuglease.DefaultDir())
			if err != nil {
				fmt.Fprintln(os.Stderr, "anon:", err)
				os.Exit(1)
			}
			allowedFile = path
			if signerID == "" {
				signerID = "local"
			}
		}
	}
	if *leasePath != "" && signerID == "" {
		if strings.HasPrefix(*signer, "github:") {
			signerID = strings.TrimPrefix(*signer, "github:")
		} else {
			signerID = "local"
		}
	}
	seedValue := *seedID
	if seedValue == "" {
		if f, err := seed.Load("seed.json"); err == nil {
			seedValue = f.Seed.ID
		}
	}

	a, err := anon.New(anon.Config{
		Name:           *name,
		Listen:         *listen,
		DebugSocket:    *debugSocket,
		LeasePath:      *leasePath,
		AllowedSigners: allowedFile,
		SignerIdentity: signerID,
		SeedID:         seedValue,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "anon:", err)
		os.Exit(1)
	}
	if err := a.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "anon:", err)
		os.Exit(1)
	}
	fmt.Printf("anon %s  %s\ntools       (none)\n", *name, a.URL())
	if a.DebugActive() {
		fmt.Printf("debug       %s (lease active, seed %s)\n", a.DebugPath(), seedValue)
	} else {
		fmt.Println("debug       disabled (no valid lease)")
	}
	fmt.Println("Ctrl+C to stop")

	sig, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-sig.Done()
	_ = a.Close()
}

// lezz mcp [--cache-url URL] [--listen host:port]
// Serves the narrow, read-only MCP wrapper. This is the first unauthenticated
// cluster layer; the real adhd tools are not exposed here.
func cmdMCP(ctx context.Context) {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	cacheURL := fs.String("cache-url", os.Getenv("LEZZ_CACHE_URL"), "PR cache base URL (default: LEZZ_CACHE_URL)")
	listen := fs.String("listen", "127.0.0.1:0", "MCP listen address")
	name := fs.String("name", "pr", "wrapper name")
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(1)
	}
	if *cacheURL == "" {
		fmt.Fprintln(os.Stderr, "mcp: --cache-url (or LEZZ_CACHE_URL) is required")
		os.Exit(1)
	}
	w, err := mcpwrapper.New(mcpwrapper.Config{Name: *name, Listen: *listen, CacheURL: *cacheURL})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		os.Exit(1)
	}
	if err := w.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		os.Exit(1)
	}
	fmt.Printf("mcp wrapper %s  %s\ntools       pr.get, pr.status (read-only)\n", *name, w.URL())
	fmt.Println("Ctrl+C to stop")

	sig, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-sig.Done()
	_ = w.Close()
}

// lezz cache [run] [--config path]
// Serves a read-through cache of GitHub PR view/diff data. Concurrent misses
// are single-flighted; fresh reads come from SQLite.
func cmdCache(ctx context.Context) {
	args := os.Args[2:]
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("cache", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a cache YAML (default: embedded github definition)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	var (
		cfg cache.Config
		err error
	)
	if *configPath == "" {
		cfg, err = cache.DefaultConfig()
	} else {
		cfg, err = cache.LoadConfig(*configPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cache:", err)
		os.Exit(1)
	}
	c, err := cache.New(cfg, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cache:", err)
		os.Exit(1)
	}
	if err := c.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "cache:", err)
		os.Exit(1)
	}
	fmt.Printf("cache %s  %s\ndb         %s\nttl        %s\n", cfg.Name, c.URL(), c.DB(), c.TTL())
	fmt.Println("Ctrl+C to stop")

	sig, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-sig.Done()
	_ = c.Close()
}

// lezz queue [run] [--config path]
// Runs a spool + FIFO doorbell in front of a rate-limited upstream. The static
// config is lezz's; consumers only see the loopback HTTP endpoint.
func cmdQueue(ctx context.Context) {
	args := os.Args[2:]
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("queue", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a queue YAML (default: embedded github definition)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	var (
		cfg queue.Config
		err error
	)
	if *configPath == "" {
		cfg, err = queue.DefaultConfig()
	} else {
		cfg, err = queue.LoadConfig(*configPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "queue:", err)
		os.Exit(1)
	}
	q, err := queue.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "queue:", err)
		os.Exit(1)
	}
	if err := q.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "queue:", err)
		os.Exit(1)
	}
	fmt.Printf("queue %s  %s\nspool      %s\nstore      %s\n", cfg.Name, q.URL(), q.Dir(), q.StoreName())
	fmt.Println("Ctrl+C to stop")

	sig, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-sig.Done()
	_ = q.Close()
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

// lezz agent setup|remove|show|build [editor]
// Wires an ACP agent into an editor's settings. Only the agent's own
// `agent_servers` member is touched; the rest of the file, including comments,
// is left byte-for-byte alone.
func cmdAgent(ctx context.Context) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lezz agent setup|remove|show|build [editor] [flags]")
		os.Exit(1)
	}
	if os.Args[2] == "build" {
		cmdAgentBuild(ctx)
		return
	}
	action := os.Args[2]
	rest := os.Args[3:]

	editor := "zed"
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		editor = rest[0]
		rest = rest[1:]
	}

	fs := flag.NewFlagSet("agent "+action, flag.ExitOnError)
	name := fs.String("name", "workspace", "the agent_servers key")
	settings := fs.String("settings", "", "override the settings file")
	dryRun := fs.Bool("dry-run", false, "show the change without writing it")
	yes := fs.Bool("yes", false, "confirm a removal")
	command := fs.String("command", "", "the agent binary (default: resolve workspace-agent)")
	root := fs.String("root", "", "the workspace root (default ~/.lezz/workspaces/<session>)")
	session := fs.String("session", "42", "the workspace session id")
	spec := fs.String("spec", "", "seed the session from this Gherkin spec")
	src := fs.String("src", "", "workspace-agent source (lab-safety checkout or agent module)")
	noBuild := fs.Bool("no-build", false, "do not fetch/build workspace-agent when it is missing")
	_ = fs.Parse(rest)

	if editor != "zed" {
		fmt.Fprintf(os.Stderr, "unknown editor %q (supported: zed)\n", editor)
		os.Exit(1)
	}

	path := *settings
	if path == "" {
		p, err := agents.ZedSettingsPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "agent:", err)
			os.Exit(1)
		}
		path = p
	}

	switch action {
	case "setup":
		cmdAgentSetup(ctx, path, *name, *command, *root, *session, *spec, *src, *dryRun, *noBuild)
	case "remove":
		cmdAgentRemove(path, *name, *dryRun, *yes)
	case "show":
		cmdAgentShow(path, *name)
	default:
		fmt.Fprintf(os.Stderr, "unknown agent action %q; use setup, remove, show, or build\n", action)
		os.Exit(1)
	}
}

func cmdAgentBuild(ctx context.Context) {
	fs := flag.NewFlagSet("agent build", flag.ContinueOnError)
	src := fs.String("src", "", "lab-safety checkout or agent module (default: ~/.lezz/src/lab-safety)")
	out := fs.String("out", "", "output path (default: ~/.lezz/bin/workspace-agent)")
	noFetch := fs.Bool("no-fetch", false, "use the existing source checkout without git fetch")
	if err := fs.Parse(os.Args[3:]); err != nil {
		os.Exit(1)
	}
	path, err := workspaceagent.Build(ctx, workspaceagent.Options{
		Source:  expandHome(*src),
		Out:     expandHome(*out),
		NoFetch: *noFetch,
		Stdout:  os.Stderr,
		Stderr:  os.Stderr,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent build:", err)
		os.Exit(1)
	}
	if rev := workspaceagent.SourceRevision(expandHome(*src)); rev != "" {
		fmt.Printf("built %s (source %s)\n", path, rev)
		return
	}
	fmt.Printf("built %s\n", path)
}

func cmdAgentSetup(ctx context.Context, path, name, command, root, session, spec, src string, dryRun, noBuild bool) {
	cmdPath, err := resolveAgentCommand(command)
	if err != nil {
		// A local path or LEZZ_WORKSPACE_AGENT was supplied: respect it and do
		// not silently build a different binary.
		if dryRun || noBuild || command != "" || os.Getenv("LEZZ_WORKSPACE_AGENT") != "" {
			fmt.Fprintln(os.Stderr, "agent setup:", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "workspace-agent not found; building from source (use --no-build to disable)...")
		built, buildErr := workspaceagent.Build(ctx, workspaceagent.Options{
			Source: expandHome(src),
			Stdout: os.Stderr,
			Stderr: os.Stderr,
		})
		if buildErr != nil {
			fmt.Fprintln(os.Stderr, "agent setup:", buildErr)
			os.Exit(1)
		}
		cmdPath = built
	}
	if root == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			fmt.Fprintln(os.Stderr, "agent setup:", homeErr)
			os.Exit(1)
		}
		root = filepath.Join(home, ".lezz", "workspaces", session)
	}
	args := []string{"-driver", "tmpdir", "-root", root, "-session", session}
	if spec != "" {
		args = append(args, "-spec", spec)
	}

	entry := agents.Entry{Name: name, Command: cmdPath, Args: args}
	res, err := agents.Upsert(path, entry, dryRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent setup:", err)
		os.Exit(1)
	}

	switch {
	case !res.Changed:
		fmt.Printf("%s is already wired in %s\n", name, path)
	case dryRun:
		b, _ := entry.JSON()
		fmt.Printf("would wire %s in %s:\n\n%s\n", name, path, b)
	default:
		fmt.Printf("wired %s in %s\n", name, path)
		fmt.Printf("  command: %s\n", cmdPath)
		fmt.Printf("  args:    %s\n", strings.Join(args, " "))
		if res.Backup != "" {
			fmt.Printf("  backup:  %s\n", res.Backup)
		}
		fmt.Println("Zed picks up settings changes without a restart.")
	}
}

func cmdAgentRemove(path, name string, dryRun, yes bool) {
	if !yes && !dryRun {
		fmt.Printf("would remove %s from %s\n", name, path)
		fmt.Println("re-run with --yes to remove it")
		os.Exit(1)
	}
	res, err := agents.Remove(path, name, dryRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent remove:", err)
		os.Exit(1)
	}
	switch {
	case !res.Changed:
		fmt.Printf("%s is not wired in %s\n", name, path)
	case dryRun:
		fmt.Printf("would remove %s from %s\n", name, path)
	default:
		fmt.Printf("removed %s from %s\n", name, path)
		if res.Backup != "" {
			fmt.Printf("  backup: %s\n", res.Backup)
		}
	}
}

func cmdAgentShow(path, name string) {
	e, ok, err := agents.Show(path, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent show:", err)
		os.Exit(1)
	}
	if !ok {
		fmt.Printf("%s is not wired in %s\n", name, path)
		return
	}
	fmt.Printf("name:    %s\n", e.Name)
	fmt.Printf("command: %s\n", e.Command)
	fmt.Printf("args:    %s\n", strings.Join(e.Args, " "))
	fmt.Printf("path:    %s\n", path)
}

// resolveAgentCommand finds the workspace agent: an explicit path, the
// environment, lezz's bin directory, or PATH. It fails with the build command
// rather than writing an entry that cannot start.
func resolveAgentCommand(explicit string) (string, error) {
	if explicit != "" {
		if p, err := resolveExecutable(explicit); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("workspace-agent %q not found (pass an existing path or a command on PATH)", explicit)
	}
	if v := os.Getenv("LEZZ_WORKSPACE_AGENT"); v != "" {
		if p, err := resolveExecutable(v); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("LEZZ_WORKSPACE_AGENT=%q not found (unset it or point it at an existing binary)", v)
	}
	if binDir, err := tools.BinDir(); err == nil {
		p := filepath.Join(binDir, "workspace-agent")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath("workspace-agent"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf(`workspace-agent not found
  build it:  lezz agent build
  or pass:   --command /path/to/workspace-agent`)
}

func resolveExecutable(v string) (string, error) {
	if st, err := os.Stat(v); err == nil && !st.IsDir() {
		return v, nil
	}
	return exec.LookPath(v)
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

// lezz auth github|status|logout
// GitHub device flow: no client secret, client ID embedded at build time, token
// stored in the platform credential store (Keychain on macOS).
func cmdAuth(ctx context.Context) {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lezz auth github|status|logout")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "github":
		cmdAuthGitHub(ctx)
	case "status":
		cmdAuthStatus(ctx)
	case "logout":
		cmdAuthLogout()
	default:
		fmt.Fprintf(os.Stderr, "unknown auth action %q; use github, status, or logout\n", os.Args[2])
		os.Exit(1)
	}
}

func cmdAuthGitHub(ctx context.Context) {
	fs := flag.NewFlagSet("auth github", flag.ContinueOnError)
	clientID := fs.String("client-id", "", "GitHub OAuth App client ID (default: embedded build value or LEZZ_GITHUB_CLIENT_ID)")
	scopes := fs.String("scopes", githubauth.DefaultScopes, "OAuth scopes")
	storeKind := fs.String("store", "auto", "credential store: auto, keychain, or file")
	noBrowser := fs.Bool("no-browser", false, "print the verification URL instead of opening a browser")
	if err := fs.Parse(os.Args[3:]); err != nil {
		os.Exit(1)
	}

	id := githubClientID(*clientID)
	if id == "" {
		fmt.Fprintln(os.Stderr, `auth github: no GitHub OAuth App client ID.

  Create an OAuth App with Device Flow enabled, then either:
    build:  go build -ldflags "-X main.buildGitHubClientID=<client-id>" ./cmd/lezz
    run:    LEZZ_GITHUB_CLIENT_ID=<client-id> lezz auth github

  The client ID is public. Device flow never uses a client secret.`)
		os.Exit(1)
	}

	store, err := credential.New(*storeKind)
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth github:", err)
		os.Exit(1)
	}
	client := githubauth.New(id)
	dc, err := client.StartDeviceFlow(ctx, *scopes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth github:", err)
		os.Exit(1)
	}
	verifyURL := dc.VerificationURIComplete
	if verifyURL == "" {
		verifyURL = dc.VerificationURI
	}
	fmt.Printf("Open %s and enter code: %s\n", verifyURL, dc.UserCode)
	if !*noBrowser {
		if browserErr := openBrowser(verifyURL); browserErr != nil {
			fmt.Fprintf(os.Stderr, "auth github: could not open browser: %v\n", browserErr)
		}
	}
	fmt.Println("Waiting for GitHub authorization...")

	token, err := client.WaitForToken(ctx, dc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth github:", err)
		os.Exit(1)
	}
	identity, err := client.FetchIdentity(ctx, token.AccessToken)
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth github:", err)
		os.Exit(1)
	}
	rec, err := githubauth.Save(store, token, identity)
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth github:", err)
		os.Exit(1)
	}

	scopesOut := strings.Join(rec.Scopes, ", ")
	if scopesOut == "" {
		scopesOut = "(none reported)"
	}
	fmt.Printf("signed in   %s (id %d)\n", rec.Login, rec.ID)
	fmt.Printf("scopes      %s\n", scopesOut)
	fmt.Printf("token       %s (%s; value not printed)\n", rec.AuthRef, rec.StoreKind)
	fmt.Printf("metadata    %s\n", githubauth.DefaultRecordPath())
	fmt.Printf("auth-ref    %s\n", rec.AuthRef)
	fmt.Printf("seed        lezz seed init --auth-ref %s\n", rec.AuthRef)
}

func cmdAuthStatus(ctx context.Context) {
	fs := flag.NewFlagSet("auth status", flag.ContinueOnError)
	storeKind := fs.String("store", "auto", "credential store: auto, keychain, or file")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(os.Args[3:]); err != nil {
		os.Exit(1)
	}

	rec, err := githubauth.LoadRecord()
	if errors.Is(err, credential.ErrNotFound) {
		if *asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"signed_in": false})
		} else {
			fmt.Println("not signed in")
		}
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth status:", err)
		os.Exit(1)
	}
	store, err := credential.New(storeKindFor(*storeKind, rec))
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth status:", err)
		os.Exit(1)
	}
	token, tokenErr := store.Get(rec.TokenService, rec.TokenAccount)
	var identity *githubauth.Identity
	var verifyErr error
	if tokenErr == nil {
		identity, verifyErr = githubauth.New("").FetchIdentity(ctx, token)
		if verifyErr == nil {
			now := time.Now().UTC()
			rec.VerifiedAt = &now
			_ = githubauth.WriteRecord(githubauth.DefaultRecordPath(), rec)
		}
	}

	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"signed_in": true,
			"record":    rec,
			"verified":  identity != nil,
			"error":     errString(tokenErr, verifyErr),
		})
		return
	}

	scopes := strings.Join(rec.Scopes, ", ")
	if scopes == "" {
		scopes = "(none reported)"
	}
	var verified string
	switch {
	case tokenErr != nil:
		verified = "no (token missing: " + tokenErr.Error() + ")"
	case verifyErr != nil:
		verified = "no (" + verifyErr.Error() + ")"
	case rec.VerifiedAt == nil:
		verified = "yes"
	default:
		verified = "yes (" + rec.VerifiedAt.Format(time.RFC3339) + ")"
	}
	fmt.Printf("login      %s\n", rec.Login)
	fmt.Printf("id         %d\n", rec.ID)
	fmt.Printf("scopes     %s\n", scopes)
	fmt.Printf("store      %s\n", rec.StoreKind)
	fmt.Printf("auth-ref   %s\n", rec.AuthRef)
	fmt.Printf("created    %s\n", rec.CreatedAt.Format(time.RFC3339))
	fmt.Printf("verified   %s\n", verified)
	if identity != nil && identity.Login != rec.Login {
		fmt.Printf("github     %s (id %d)\n", identity.Login, identity.ID)
	}
}

func cmdAuthLogout() {
	fs := flag.NewFlagSet("auth logout", flag.ContinueOnError)
	storeKind := fs.String("store", "auto", "credential store: auto, keychain, or file")
	if err := fs.Parse(os.Args[3:]); err != nil {
		os.Exit(1)
	}
	rec, _ := githubauth.LoadRecord()
	store, err := credential.New(storeKindFor(*storeKind, rec))
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth logout:", err)
		os.Exit(1)
	}
	if err := githubauth.Logout(store); err != nil {
		fmt.Fprintln(os.Stderr, "auth logout:", err)
		os.Exit(1)
	}
	fmt.Println("signed out; GitHub token and metadata removed")
}

func storeKindFor(flagValue string, rec *githubauth.Record) string {
	if flagValue != "" && flagValue != "auto" {
		return flagValue
	}
	if rec != nil && strings.HasPrefix(rec.StoreKind, "file") {
		return "file"
	}
	return "auto"
}

func errString(errs ...error) string {
	for _, err := range errs {
		if err != nil {
			return err.Error()
		}
	}
	return ""
}

func githubClientID(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := strings.TrimSpace(os.Getenv("LEZZ_GITHUB_CLIENT_ID")); env != "" {
		return env
	}
	return buildGitHubClientID
}

func openBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		//nolint:gosec // G204: argv-only exec, no shell; rawURL is GitHub's own OAuth verification URL.
		cmd = exec.Command("open", rawURL)
	case "linux":
		//nolint:gosec // G204: argv-only exec, no shell; rawURL is GitHub's own OAuth verification URL.
		cmd = exec.Command("xdg-open", rawURL)
	case "windows":
		//nolint:gosec // G204: argv-only exec, no shell; rawURL is GitHub's own OAuth verification URL.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
	return cmd.Start()
}

// normalizeArgs makes a Finder/LaunchServices launch behave like a normal app.
// macOS appends -psn_<...> to the first process; it is not a lezz command. A
// bundle launched with no arguments runs the base cluster.
func normalizeArgs(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	inBundle := strings.Contains(filepath.ToSlash(argv[0]), ".app/Contents/MacOS/")
	out := make([]string, 0, len(argv))
	out = append(out, argv[0])
	for _, arg := range argv[1:] {
		if strings.HasPrefix(arg, "-psn_") {
			continue
		}
		out = append(out, arg)
	}
	if len(out) == 1 && inBundle {
		out = append(out, "auto")
	}
	return out
}

// lezz app install|open|info|uninstall
// Wraps the lezz binary in a macOS .app so macOS has a stable identity for TCC
// permissions and passkeys. The app defaults to the base cluster.
func cmdApp() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lezz app install|open|info|uninstall")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "install":
		cmdAppInstall()
	case "open":
		cmdAppOpen()
	case "info":
		cmdAppInfo()
	case "uninstall":
		cmdAppUninstall()
	default:
		fmt.Fprintf(os.Stderr, "unknown app action %q; use install, open, info, or uninstall\n", os.Args[2])
		os.Exit(1)
	}
}

func cmdAppInstall() {
	fs := flag.NewFlagSet("app install", flag.ContinueOnError)
	dest := fs.String("dest", "", "app bundle path (default ~/Applications/Lezz.app)")
	name := fs.String("name", appbundle.DefaultName, "bundle name")
	bundleID := fs.String("bundle-id", appbundle.DefaultBundleID, "bundle identifier")
	identity := fs.String("identity", appbundle.DefaultIdentity(), "codesign identity (default ad-hoc)")
	entitlements := fs.String("entitlements", "", "codesign entitlements plist")
	scheme := fs.String("url-scheme", "lezz", "URL scheme to register (empty to disable)")
	dock := fs.Bool("dock", false, "show a Dock icon instead of running as a background app")
	force := fs.Bool("force", false, "replace an existing bundle")
	bin := fs.String("bin", "", "binary to wrap (default: this lezz)")
	if err := fs.Parse(os.Args[3:]); err != nil {
		os.Exit(1)
	}

	info, err := appbundle.Install(appbundle.Config{
		AppPath:      expandHome(*dest),
		Name:         *name,
		BundleID:     *bundleID,
		Version:      version(),
		Binary:       expandHome(*bin),
		Identity:     *identity,
		Entitlements: expandHome(*entitlements),
		URLScheme:    *scheme,
		Dock:         *dock,
		Force:        *force,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "app install:", err)
		os.Exit(1)
	}

	signature := info.Signature
	if info.Authority != "" {
		signature = info.Authority
	}
	if signature == "" {
		signature = "unknown"
	}
	fmt.Printf("installed %s\n", info.Path)
	fmt.Printf("bundle    %s\n", info.BundleID)
	fmt.Printf("version   %s (%s)\n", info.Version, info.Build)
	fmt.Printf("signature %s\n", signature)
	fmt.Printf("launch    open -a %s\n", info.Path)
	fmt.Println()
	fmt.Println("Permissions: System Settings → Privacy & Security → Local Network → enable Lezz.")
	fmt.Println("             Add Lezz.app to Full Disk Access if lezz must read protected paths.")
	if *identity == "-" {
		fmt.Println()
		fmt.Println("Note: ad-hoc signature. TCC grants can drift when the app is rebuilt;")
		fmt.Println("      pass --identity \"Developer ID Application: …\" for a stable identity.")
	}
}

func cmdAppOpen() {
	fs := flag.NewFlagSet("app open", flag.ContinueOnError)
	app := fs.String("app", "", "app bundle path (default ~/Applications/Lezz.app)")
	wait := fs.Bool("wait", false, "wait for the app to exit")
	newInstance := fs.Bool("new", false, "start a new instance")
	if err := fs.Parse(os.Args[3:]); err != nil {
		os.Exit(1)
	}
	path := expandHome(*app)
	if path == "" {
		p, err := appbundle.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "app open:", err)
			os.Exit(1)
		}
		path = p
	}
	args := fs.Args()
	if err := appbundle.Launch(path, args, *wait, *newInstance); err != nil {
		fmt.Fprintln(os.Stderr, "app open:", err)
		os.Exit(1)
	}
	if len(args) == 0 {
		fmt.Printf("opened %s (base cluster)\n", path)
	} else {
		fmt.Printf("opened %s (args: %s)\n", path, strings.Join(args, " "))
	}
}

func cmdAppInfo() {
	fs := flag.NewFlagSet("app info", flag.ContinueOnError)
	app := fs.String("app", "", "app bundle path (default ~/Applications/Lezz.app)")
	if err := fs.Parse(os.Args[3:]); err != nil {
		os.Exit(1)
	}
	path := expandHome(*app)
	if path == "" {
		p, err := appbundle.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "app info:", err)
			os.Exit(1)
		}
		path = p
	}
	info, err := appbundle.ReadInfo(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "app info:", err)
		os.Exit(1)
	}
	signature := info.Signature
	if info.Authority != "" {
		signature = info.Authority
	}
	if signature == "" {
		signature = "unknown"
	}
	if info.TeamID != "" && info.TeamID != "not set" {
		signature += " (team " + info.TeamID + ")"
	}
	ui := "background (LSUIElement)"
	if !info.Background {
		ui = "dock"
	}
	running := "no"
	if len(info.Running) > 0 {
		running = joinPIDs(info.Running)
	}
	fmt.Printf("path        %s\n", info.Path)
	fmt.Printf("bundle      %s\n", info.BundleID)
	fmt.Printf("version     %s (%s)\n", info.Version, info.Build)
	fmt.Printf("executable  %s\n", info.Executable)
	fmt.Printf("ui          %s\n", ui)
	fmt.Printf("signature   %s\n", signature)
	fmt.Printf("running     %s\n", running)
	fmt.Printf("local net   %s\n", info.LocalNetwork)
	fmt.Printf("bonjour     %s\n", strings.Join(info.Bonjour, ", "))
}

func cmdAppUninstall() {
	fs := flag.NewFlagSet("app uninstall", flag.ContinueOnError)
	app := fs.String("app", "", "app bundle path (default ~/Applications/Lezz.app)")
	force := fs.Bool("force", false, "stop a running app and remove it")
	if err := fs.Parse(os.Args[3:]); err != nil {
		os.Exit(1)
	}
	path := expandHome(*app)
	if path == "" {
		p, err := appbundle.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "app uninstall:", err)
			os.Exit(1)
		}
		path = p
	}
	if err := appbundle.Uninstall(path, *force); err != nil {
		fmt.Fprintln(os.Stderr, "app uninstall:", err)
		os.Exit(1)
	}
	fmt.Printf("removed %s\n", path)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func joinPIDs(pids []int) string {
	parts := make([]string, len(pids))
	for i, pid := range pids {
		parts[i] = fmt.Sprint(pid)
	}
	return strings.Join(parts, ", ")
}

func usage() {
	fmt.Print(`lezz — self-updating host for adhd, ocd-smoke-alarm, and tuner

Usage:
  lezz demo                          Launch a self-contained demo cluster
  lezz auto                          Launch a base cluster — empty and awaiting, auto-installs tools, reuses an existing registry
  lezz queue [--config file]         Run a spool + FIFO queue sidecar for a rate-limited upstream
  lezz cache [--config file]         Serve a read-through cache of GitHub PR view/diff data
  lezz seed init [--surface base]    Create seed.json (project id, surface, auth ref)
  lezz see                           Read seed.json, show the 42i start and surface
  lezz debug keygen|lease|fetch-keys Debug lease keys and signing
  lezz anon --lease FILE             Empty proxy; debug socket only with a valid lease
  lezz anon [--listen host:port]     Serve the anonymous MCP surface (empty tool list)
  lezz app install [--dock]          Build and sign Lezz.app for TCC permissions/passkeys
  lezz app open [-- args...]         Launch Lezz.app through LaunchServices (default: auto)
  lezz app info                      Show the bundle identity and signature
  lezz app uninstall [--force]       Remove Lezz.app
  lezz auth github                   Sign in with GitHub device flow (token in keychain)
  lezz auth status                   Show the stored GitHub identity
  lezz auth logout                   Delete the stored GitHub token
  lezz mcp [--cache-url URL]         Serve the read-only wrapper (debug/trusted)
  lezz run <tool> [args...]          Replace lezz with the tool (exec)
  lezz start <tool> [args...]        Spawn the tool as a child process (wait)
  lezz install <tool>                Download and install a managed tool
  lezz purge                         Remove services, binaries, and Go caches for all managed tools
  lezz update                        Check for and apply lezz self-update
  lezz service install <tool>        Configure launchd service for a tool
  lezz service remove <tool>         Remove daemon config for a tool
  lezz service list                  List all installed lezz services
  lezz service purge                 Unload and remove all lezz services
  lezz agent setup [editor]          Wire an ACP agent into an editor's settings
  lezz agent build [--src DIR]       Fetch/build workspace-agent into ~/.lezz/bin
  lezz agent remove [editor]         Remove it (requires --yes)
  lezz agent show [editor]           Show the wired agent
  lezz version                       Print version

Managed tools: lezz, adhd, ocd-smoke-alarm, tuner
`)
}
