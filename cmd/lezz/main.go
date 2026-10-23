package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"github.com/james-gibson/lezz.go/internal/sea"
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

	case "prod":
		cmdProd(ctx)
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

	case "sea":
		cmdSea(ctx)
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

	case "tools":
		cmdTools(ctx)
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

	case "-h", "-help", "--h", "--help", "help":
		// A help request is not an error and must not have a side effect.
		// `lezz help <command>` describes one command; anything else prints
		// the full list.
		if rest := os.Args[2:]; len(rest) > 0 {
			if _, ok := lookupCommand(strings.Join(rest, " ")); ok {
				printCommandUsage(os.Stdout, strings.Join(rest, " "), nil)
				return
			}
		}
		usage()

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

// lezz demo [start|stop|status|restart] [--*-port N]
//
// Bare `lezz demo` runs the cluster in the foreground. `start` runs the same
// cluster detached, publishing ~/.lezz/demo-state.json so `status` and `stop`
// can reach it from any terminal.
func cmdDemo(ctx context.Context) {
	args := os.Args[2:]
	if len(args) > 0 {
		switch args[0] {
		case "start":
			cmdDemoStart(ctx, args[1:])
			return
		case "stop":
			cmdDemoStop(args[1:])
			return
		case "status":
			cmdDemoStatus(ctx, args[1:])
			return
		case "restart":
			cmdDemoRestart(ctx, args[1:])
			return
		}
	}

	fs, flags := newDemoFlagSet("demo")
	if !parseOrHelp(fs, args) {
		return
	}
	noPositional("demo", fs)
	if err := demo.Run(ctx, flags.options()); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// cmdDemoStart runs the cluster detached and waits until it publishes its
// endpoints.
func cmdDemoStart(ctx context.Context, args []string) {
	fs, flags := newDemoFlagSet("demo start")
	if !parseOrHelp(fs, args) {
		return
	}
	noPositional("demo start", fs)

	state, err := demo.StartDetached(ctx, flags.options())
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo start:", err)
		os.Exit(1)
	}
	printDemoState(state)
	if logPath, logErr := demo.LogPath(); logErr == nil {
		fmt.Printf("\nlogs         %s\n", logPath)
	}
	fmt.Print("stop         lezz demo stop\n")
}

func cmdDemoStop(args []string) {
	if helpOnly("demo stop", args) {
		return
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "demo stop: unexpected argument %q\n", args[0])
		os.Exit(2)
	}
	if _, ok := demo.RunningState(); !ok {
		fmt.Println("demo: not running")
		return
	}
	if err := demo.Stop(15 * time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "demo stop:", err)
		os.Exit(1)
	}
	fmt.Println("demo stopped")
}

func cmdDemoStatus(ctx context.Context, args []string) {
	if helpOnly("demo status", args) {
		return
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "demo status: unexpected argument %q\n", args[0])
		os.Exit(2)
	}

	state, ok := demo.RunningState()
	if !ok {
		fmt.Println("demo: not running")
		os.Exit(1)
	}
	printDemoState(state)
	fmt.Printf("started      %s\n", state.StartedAt.Local().Format(time.RFC3339))

	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	fmt.Println()
	for _, h := range demo.Probe(probeCtx, state) {
		mark := "down"
		if h.OK {
			mark = "ok"
		}
		detail := ""
		if !h.OK && h.Detail != "" {
			detail = " (" + h.Detail + ")"
		}
		fmt.Printf("  %-10s %-4s %s%s\n", h.Name, mark, h.Endpoint, detail)
	}
	if state.LogDir != "" {
		fmt.Printf("\nlogs         %s/{alarm-a,alarm-b,adhd}.log\n", state.LogDir)
	}
}

func cmdDemoRestart(ctx context.Context, args []string) {
	fs, flags := newDemoFlagSet("demo restart")
	if !parseOrHelp(fs, args) {
		return
	}
	noPositional("demo restart", fs)

	running, ok := demo.RunningState()
	if ok {
		fmt.Printf("stopping demo (pid %d)...\n", running.PID)
	}
	if err := demo.Stop(15 * time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "demo restart:", err)
		os.Exit(1)
	}

	state, err := demo.StartDetached(ctx, flags.options())
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo restart:", err)
		os.Exit(1)
	}
	printDemoState(state)
}

// demoFlags holds the port pins shared by demo, demo start and demo restart.
type demoFlags struct {
	alarmA int
	alarmB int
	adhd   int
}

func newDemoFlagSet(name string) (*flag.FlagSet, *demoFlags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	flags := &demoFlags{}
	fs.IntVar(&flags.alarmA, "alarm-a-port", 0, "pin the alarm-a health port (default: reuse the last run's port when it is free)")
	fs.IntVar(&flags.alarmB, "alarm-b-port", 0, "pin the alarm-b health port (default: reuse the last run's port when it is free)")
	fs.IntVar(&flags.adhd, "adhd-port", 0, "pin the adhd MCP port (default: reuse the last run's port when it is free)")
	return fs, flags
}

func (f *demoFlags) options() demo.Options {
	return demo.Options{AlarmAPort: f.alarmA, AlarmBPort: f.alarmB, ADHDPort: f.adhd}
}

// noPositional rejects bare arguments after the flags.
func noPositional(cmd string, fs *flag.FlagSet) {
	if rest := fs.Args(); len(rest) > 0 {
		fmt.Fprintf(os.Stderr, "%s: unexpected argument %q\n", cmd, rest[0])
		os.Exit(2)
	}
}

// printDemoState is the endpoint block shared by start, status and restart.
func printDemoState(state demo.State) {
	fmt.Printf("demo cluster running (pid %d)\n\n", state.PID)
	fmt.Printf("alarm-a      %s/status\n", state.Cluster.AlarmA)
	fmt.Printf("alarm-b      %s/status\n", state.Cluster.AlarmB)
	fmt.Printf("adhd MCP     %s\n", state.Cluster.AdhdMCP)
	fmt.Printf("discovery    %s\n", state.Discovery)
	if state.StableConfig != "" {
		fmt.Printf("dashboard    adhd --config %s\n", state.StableConfig)
	}
}

// lezz auto
// Launches a base cluster in auto mode: installs any missing managed tools,
// reuses an existing discovery registry, and starts the cluster empty and
// awaiting — targets/config arrive via the server's auto-refresh and remote
// update paths instead of a hardcoded config.
func cmdAuto(ctx context.Context) {
	if helpOnly("auto", os.Args[2:]) {
		return
	}
	if err := noArguments("auto", os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "auto:", err)
		os.Exit(2)
	}
	if err := demo.RunAuto(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "auto:", err)
		os.Exit(1)
	}
}

// lezz prod [--isotope NAME] [--base-port N] [--rotate]
// Launches a production-shaped cluster: three full nodes (A, B, C), each an
// alarm plus a headless adhd, with node A's registry as the anchor on 19100.
// SIGHUP (or --rotate) installs the next generation as a launchd service and
// hands the fixed ports over to it.
func cmdProd(ctx context.Context) {
	// A requested help goes to stdout like every other command; a parse error
	// still prints to stderr, where the caller's own message is.
	help := os.Stderr
	if helpRequested(os.Args[2:]) {
		help = os.Stdout
	}
	opts, err := demo.ParseProdArgs(os.Args[2:], help)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "prod:", err)
		os.Exit(2)
	}
	if err := demo.RunProd(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, "prod:", err)
		os.Exit(1)
	}
}

// lezz seed init [--surface base] [--auth-ref env:NAME] [--name NAME] [--force]
// Creates seed.json: a stable project id, the surface to join, and an external
// auth reference. It never writes a secret and never touches a GitHub identity.
func cmdSeed() {
	if helpOnly("seed", os.Args[2:]) {
		return
	}
	if len(os.Args) < 3 || os.Args[2] != "init" {
		fmt.Fprintln(os.Stderr, "usage: lezz seed init [--surface base] [--auth-ref env:LEZZ_CLUSTER_KEY] [--name NAME] [--force]")
		os.Exit(1)
	}
	fs := flag.NewFlagSet("seed init", flag.ContinueOnError)
	surface := fs.String("surface", "base", "cluster surface to join or create")
	authRef := fs.String("auth-ref", "env:LEZZ_CLUSTER_KEY", "external auth reference (env:NAME, file:/path, keychain:NAME)")
	name := fs.String("name", "", "project name (default: current folder)")
	force := fs.Bool("force", false, "overwrite an existing seed.json")
	if !parseOrHelp(fs, os.Args[3:]) {
		return
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
	if helpOnly("debug", os.Args[2:]) {
		return
	}
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lezz debug keygen|lease|fetch-keys")
		os.Exit(1)
	}
	switch os.Args[2] {
	case "keygen":
		fs := flag.NewFlagSet("debug keygen", flag.ContinueOnError)
		dir := fs.String("dir", debuglease.DefaultDir(), "key directory")
		if !parseOrHelp(fs, os.Args[3:]) {
			return
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
		if !parseOrHelp(fs, os.Args[3:]) {
			return
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
		if !parseOrHelp(fs, os.Args[3:]) {
			return
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
	if !parseOrHelp(fs, os.Args[2:]) {
		return
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
	if !parseOrHelp(fs, os.Args[2:]) {
		return
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
	if !parseOrHelp(fs, os.Args[2:]) {
		return
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
	if !parseOrHelp(fs, args) {
		return
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
	if !parseOrHelp(fs, args) {
		return
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
	if helpOnly("run", os.Args[2:]) {
		return
	}
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
	if helpOnly("install", os.Args[2:]) {
		return
	}
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
		fmt.Print(pathHint(binDir, os.Getenv("SHELL")))
	}
}

// lezz update
//
// lezz itself is self-updated from its own releases; every other managed tool
// is version-checked against its latest GitHub release and reinstalled when it
// is older. A failure in one half does not silently cancel the other: both are
// attempted and the exit status reflects any failure.
func cmdUpdate(ctx context.Context) {
	if helpOnly("update", os.Args[2:]) {
		return
	}
	if err := noArguments("update", os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "update:", err)
		fmt.Fprintln(os.Stderr, "to upgrade one tool: lezz tools upgrade <tool>")
		os.Exit(2)
	}
	fmt.Printf("lezz %s — checking for updates...\n", version())
	ok := true

	latest, hasUpdate, err := selfupdate.Check(ctx, version())
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "lezz update check failed:", err)
		ok = false
	case !hasUpdate:
		fmt.Printf("lezz already up to date (%s)\n", version())
	default:
		fmt.Printf("new lezz version available: %s → applying...\n", latest)
		applied, applyErr := selfupdate.Apply(ctx, version())
		if applyErr != nil {
			fmt.Fprintln(os.Stderr, "lezz update failed:", applyErr)
			ok = false
		} else {
			fmt.Printf("updated lezz to %s — restart lezz to use the new version\n", applied)
		}
	}

	fmt.Println("\nmanaged tools:")
	if !upgradeManagedTools(ctx, nil) {
		ok = false
	}

	if !ok {
		os.Exit(1)
	}
}

// lezz tools upgrade [tool...]
func cmdTools(ctx context.Context) {
	if helpOnly("tools", os.Args[2:]) {
		return
	}
	if len(os.Args) < 3 {
		printCommandUsage(os.Stderr, "tools", nil)
		os.Exit(1)
	}

	switch action := os.Args[2]; action {
	case "upgrade":
		if helpOnly("tools upgrade", os.Args[3:]) {
			return
		}
		names := os.Args[3:]
		if len(names) == 0 {
			fmt.Println("checking managed tools for updates...")
		}
		if !upgradeManagedTools(ctx, names) {
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown tools action %q; use upgrade\n", action)
		os.Exit(1)
	}
}

// upgradeManagedTools version-checks the named tools (all non-lezz managed
// tools when names is empty) and reinstalls any that are older than their latest
// release. It returns false when any tool failed.
func upgradeManagedTools(ctx context.Context, names []string) bool {
	results, err := tools.UpgradeAll(ctx, names)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tools upgrade:", err)
		return false
	}

	ok := true
	for i := range results {
		r := &results[i]
		switch {
		case r.Err != nil:
			fmt.Fprintf(os.Stderr, "  %-16s failed: %v\n", r.Tool.Name, r.Err)
			ok = false
		case r.Changed:
			from := r.Installed
			if from == "" {
				from = r.Note
			}
			fmt.Printf("  %-16s %s → %s\n", r.Tool.Name, from, r.Latest)
		default:
			fmt.Printf("  %-16s up to date (%s)\n", r.Tool.Name, r.Latest)
		}
	}
	return ok
}

// lezz start <tool> [args...]
// Spawns the tool as a child process and waits for it to exit.
// Unlike "lezz run", lezz stays alive as the parent (useful for scripting clusters).
func cmdStart() {
	if helpOnly("start", os.Args[2:]) {
		return
	}
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
	if helpOnly("service", os.Args[2:]) {
		return
	}
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: lezz service install|remove|list|purge [tool] [profile]")
		os.Exit(1)
	}
	action := os.Args[2]
	if helpRequested(os.Args[3:]) {
		printCommandUsage(os.Stdout, "service "+action, nil)
		return
	}

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
	positional, force := takeFlag(os.Args[3:], "--force")
	if force && action != "install" {
		fmt.Fprintln(os.Stderr, "--force is only meaningful for `lezz service install`")
		os.Exit(2)
	}
	if len(positional) < 1 {
		if action == "install" {
			fmt.Fprintln(os.Stderr, "usage: lezz service install [--force] <tool> [profile]")
		} else {
			fmt.Fprintln(os.Stderr, "usage: lezz service remove <tool> [profile]")
		}
		os.Exit(1)
	}
	toolName := positional[0]
	profileName := "idle"
	if len(positional) >= 2 {
		profileName = positional[1]
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
		notes, err := service.Install(t, p, binPath, force)
		for _, note := range notes {
			fmt.Fprintf(os.Stderr, "service install: %s\n", note)
		}
		if err != nil {
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
	if helpOnly("purge", os.Args[2:]) {
		return
	}
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
	if helpOnly("agent", os.Args[2:]) {
		return
	}
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

	fs := flag.NewFlagSet("agent "+action, flag.ContinueOnError)
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
	if !parseOrHelp(fs, rest) {
		return
	}

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
	if !parseOrHelp(fs, os.Args[3:]) {
		return
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
	if st, err := os.Stat(v); err == nil && !st.IsDir() { //nolint:gosec // --command is the user asking for this exact path
		return v, nil
	}
	return exec.LookPath(v)
}

func cmdVersion() {
	if helpOnly("version", os.Args[2:]) {
		return
	}
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

// pathHint is the guidance printed when lezz's bin directory is not on PATH.
//
// It never hands over an unguarded append. `echo ... >> ~/.zshrc` is safe to
// read and dangerous to paste twice: the print was already gated on PATH, but
// the edit was not, so one real ~/.zshrc collected 294 identical exports
// (james-gibson/lezz.go#15). The permanent line is therefore gated on the file's
// contents, and the session line is separate so nothing has to be appended at
// all.
func pathHint(binDir, shellPath string) string {
	exportLine := fmt.Sprintf("export PATH=\"%s:$PATH\"", binDir)

	var b strings.Builder
	b.WriteString("\nAdd lezz's bin directory to your PATH:\n\n")
	fmt.Fprintf(&b, "  this shell:  %s\n", exportLine)

	profile := shellProfile(shellPath)
	if profile == "" {
		b.WriteString("\n  add that line to your shell profile to make it permanent.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\n  once, in %s — appends only if the line is absent:\n", profile)
	fmt.Fprintf(&b, "  %s\n", pathProfileCommand(exportLine, profile))
	return b.String()
}

// pathProfileCommand is the copy-pasteable profile edit: grep decides, and the
// echo only runs when the line is missing. Running it any number of times
// leaves exactly one line. -s keeps the first run quiet when the profile does
// not exist yet.
func pathProfileCommand(exportLine, profile string) string {
	return fmt.Sprintf("grep -qsF '%s' %s || echo '%s' >> %s", exportLine, profile, exportLine, profile)
}

// shellProfile names the startup file that holds a POSIX export line, or "" when
// this hint cannot be trusted for the shell — fish, for one, does not take
// `export PATH=...`, so it gets the session line only.
func shellProfile(shellPath string) string {
	if strings.TrimSpace(shellPath) == "" {
		return "~/.profile"
	}
	switch shell := filepath.Base(shellPath); {
	case strings.Contains(shell, "zsh"):
		return "~/.zshrc"
	case strings.Contains(shell, "bash"):
		return "~/.bash_profile"
	case shell == "sh" || shell == "dash" || shell == "ksh":
		return "~/.profile"
	default:
		return ""
	}
}

// lezz auth github|status|logout
// GitHub device flow: no client secret, client ID embedded at build time, token
// stored in the platform credential store (Keychain on macOS).
func cmdAuth(ctx context.Context) {
	if helpOnly("auth", os.Args[2:]) {
		return
	}
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
	if !parseOrHelp(fs, os.Args[3:]) {
		return
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
	if !parseOrHelp(fs, os.Args[3:]) {
		return
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
	if !parseOrHelp(fs, os.Args[3:]) {
		return
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

// lezz sea [--install] [--check] [--force] [--allow-unnotarized] [--channel dev]
//
// Launches the lezz.sea dashboard. When the app is missing it is downloaded
// from the signed dev release and verified before anything is launched: the
// archive's SHA-256, then the bundle's Developer ID signature, team, hardened
// runtime, secure timestamp and notarization ticket. Nothing in that path
// trusts the download — the signature is the trust anchor, and a build that
// fails it is refused rather than installed.
func cmdSea(ctx context.Context) {
	fs := flag.NewFlagSet("sea", flag.ContinueOnError)
	installOnly := fs.Bool("install", false, "install or update without launching")
	check := fs.Bool("check", false, "report what is installed and exit")
	force := fs.Bool("force", false, "reinstall even when a verified copy is installed")
	allowUnnotarized := fs.Bool("allow-unnotarized", false, "install a signed build with no notarization ticket (dev only)")
	channel := fs.String("channel", sea.Channel, "release channel to install from")
	if !parseOrHelp(fs, os.Args[2:]) {
		return
	}
	if len(fs.Args()) > 0 {
		fmt.Fprintf(os.Stderr, "sea: unexpected argument %q\n", fs.Args()[0])
		printCommandUsage(os.Stderr, "sea", nil)
		os.Exit(1)
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "sea: lezz.sea is a macOS application; this host is "+runtime.GOOS)
		os.Exit(1)
	}

	if *check {
		path, ok := sea.Installed("")
		if !ok {
			fmt.Printf("%s is not installed\n", sea.AppName)
			return
		}
		info, err := sea.Inspect(ctx, path, *allowUnnotarized)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sea: %s does not verify: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Printf("installed %s\n", info.Path)
		fmt.Printf("  version   %s (%s)\n", info.Version, info.Build)
		fmt.Printf("  signature %s\n", info.Authority)
		fmt.Printf("  team      %s\n", info.TeamID)
		fmt.Printf("  notarized %t\n", info.Notarized)
		return
	}

	res, err := sea.Ensure(ctx, sea.Options{
		Channel:          *channel,
		Force:            *force,
		AllowUnnotarized: *allowUnnotarized,
		Log:              func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sea:", err)
		os.Exit(1)
	}
	if res.Downloaded {
		fmt.Printf("installed %s %s (%s)\n", sea.AppName, res.Version, res.Build)
	}
	if *installOnly {
		fmt.Printf("  %s\n", res.Path)
		return
	}
	if err := appbundle.Launch(res.Path, nil, false, false); err != nil {
		fmt.Fprintln(os.Stderr, "sea:", err)
		os.Exit(1)
	}
	fmt.Printf("launched %s\n", res.Path)
}

// lezz app install|open|info|uninstall
// Wraps the lezz binary in a macOS .app so macOS has a stable identity for TCC
// permissions and passkeys. The app defaults to the base cluster.
func cmdApp() {
	if helpOnly("app", os.Args[2:]) {
		return
	}
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
	if !parseOrHelp(fs, os.Args[3:]) {
		return
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
	if !parseOrHelp(fs, os.Args[3:]) {
		return
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
	if !parseOrHelp(fs, os.Args[3:]) {
		return
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
	if !parseOrHelp(fs, os.Args[3:]) {
		return
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

// --- help ---
//
// A help request prints usage and does nothing else. Every command asks these
// helpers before it parses its own flags or starts any work, so `lezz demo
// --help` cannot launch a cluster.

// helpRequested reports whether args ask for usage. It stops at the first
// argument that is not a flag, which is also where the flag package stops
// parsing: lezz's own options come first, so `lezz run adhd -h` still hands
// -h to adhd.
func helpRequested(args []string) bool {
	for _, a := range args {
		if a == "-" || a == "--" || !strings.HasPrefix(a, "-") {
			return false
		}
		switch strings.TrimLeft(a, "-") {
		case "h", "help":
			return true
		}
	}
	return false
}

// parseOrHelp parses a command's flags, or prints the command's usage when
// args ask for it. It returns false when usage was printed, so the caller
// returns before doing any work.
func parseOrHelp(fs *flag.FlagSet, args []string) bool {
	// The flag package's own usage is a bare flag list; this file prints the
	// command's block instead. Silence it so a help flag that reaches Parse
	// cannot print both.
	fs.Usage = func() {}
	if helpRequested(args) {
		printCommandUsage(os.Stdout, fs.Name(), fs)
		return false
	}
	if err := fs.Parse(args); err != nil {
		// A help flag can still reach Parse when it follows a value, e.g.
		// `lezz queue --config /p --help`.
		if errors.Is(err, flag.ErrHelp) {
			printCommandUsage(os.Stdout, fs.Name(), fs)
			return false
		}
		os.Exit(1)
	}
	return true
}

// helpOnly is parseOrHelp for commands that take no flags of their own.
func helpOnly(path string, args []string) bool {
	if !helpRequested(args) {
		return false
	}
	printCommandUsage(os.Stdout, path, nil)
	return true
}

// noArguments rejects positional arguments for a command that accepts none.
// It keeps a mistyped flag from being silently ignored.
func noArguments(path string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("unexpected argument %q; lezz %s takes no arguments (try lezz %s --help)", args[0], path, path)
}

// takeFlag removes name (or its single-dash spelling) from args and reports
// whether it was there. `lezz service` takes positionals only, so --force may
// appear anywhere among them.
func takeFlag(args []string, name string) (rest []string, found bool) {
	rest = make([]string, 0, len(args))
	short := "-" + strings.TrimLeft(name, "-")
	for _, a := range args {
		if a == name || a == short {
			found = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, found
}

// commandDoc is one row of the command table. usage() and `lezz <cmd> --help`
// both read it, so the two cannot describe a command differently.
type commandDoc struct {
	// path is what the user types after the program name: "demo", or
	// "app install". A parent entry ("app") answers for its sub-actions.
	path string
	// display is the line usage() prints, arguments included.
	display string
	// synopsis is the one-line description.
	synopsis string
	// listed marks the rows usage() prints; parents and sub-actions are
	// reachable through `lezz <cmd> --help` without cluttering the top level.
	listed bool
}

// commandDocs is the single source for the command list and for per-command
// help. Order is the order usage() prints.
var commandDocs = []commandDoc{
	{"demo", "lezz demo [flags]", "Launch a self-contained demo cluster in the foreground", true},
	{"demo start", "lezz demo start [flags]", "Start the demo cluster detached", true},
	{"demo stop", "lezz demo stop", "Stop a running demo cluster", true},
	{"demo status", "lezz demo status", "Show a running demo cluster and probe its endpoints", true},
	{"demo restart", "lezz demo restart [flags]", "Restart the demo cluster, reusing the same ports when free", true},
	{"auto", "lezz auto", "Launch a base cluster — empty and awaiting, auto-installs tools, reuses an existing registry", true},
	{"prod", "lezz prod [flags]", "Launch a prod cluster — mirrored alarm_a, invisible fallback; SIGHUP rotates it as a launchd service", true},
	{"queue", "lezz queue [--config file]", "Run a spool + FIFO queue sidecar for a rate-limited upstream", true},
	{"cache", "lezz cache [--config file]", "Serve a read-through cache of GitHub PR view/diff data", true},
	{"seed", "lezz seed init", "Create seed.json: a stable project id, the surface to join, and an external auth reference", false},
	{"seed init", "lezz seed init [--surface base]", "Create seed.json (project id, surface, auth ref)", true},
	{"see", "lezz see", "Read seed.json, show the 42i start and surface", true},
	{"sea", "lezz sea [--install|--check]", "Launch lezz.sea, installing it from the signed dev release when missing", true},
	{"debug", "lezz debug keygen|lease|fetch-keys", "Debug lease keys and signing", true},
	{"anon", "lezz anon [--lease FILE] [--listen host:port]", "Serve the anonymous MCP surface (empty tool list); the debug socket needs a valid lease", true},
	{"app", "lezz app install|open|info|uninstall", "Wrap the lezz binary in a macOS .app: a stable identity for TCC permissions and passkeys", false},
	{"app install", "lezz app install [--dock]", "Build and sign Lezz.app for TCC permissions/passkeys", true},
	{"app open", "lezz app open [-- args...]", "Launch Lezz.app through LaunchServices (default: auto)", true},
	{"app info", "lezz app info", "Show the bundle identity and signature", true},
	{"app uninstall", "lezz app uninstall [--force]", "Remove Lezz.app", true},
	{"auth", "lezz auth github|status|logout", "GitHub device-flow auth; the token lives in the platform credential store", false},
	{"auth github", "lezz auth github", "Sign in with GitHub device flow (token in keychain)", true},
	{"auth status", "lezz auth status", "Show the stored GitHub identity", true},
	{"auth logout", "lezz auth logout", "Delete the stored GitHub token", true},
	{"mcp", "lezz mcp [--cache-url URL]", "Serve the read-only wrapper (debug/trusted)", true},
	{"run", "lezz run <tool> [args...]", "Replace lezz with the tool (exec)", true},
	{"start", "lezz start <tool> [args...]", "Spawn the tool as a child process (wait)", true},
	{"install", "lezz install <tool>", "Download and install a managed tool", true},
	{"tools", "lezz tools upgrade [tool...]", "Upgrade managed tools to their latest GitHub releases", false},
	{"tools upgrade", "lezz tools upgrade [tool...]", "Upgrade managed tools to their latest GitHub releases", true},
	{"purge", "lezz purge", "Remove services, binaries, and Go caches for all managed tools", true},
	{"update", "lezz update", "Update lezz and all managed tools", true},
	{"service", "lezz service install|remove|list|purge", "Configure launchd services for managed tools", false},
	{"service install", "lezz service install [--force] <tool> [profile]", "Install a launchd service for a tool, refusing a port another process owns", true},
	{"service remove", "lezz service remove <tool>", "Remove daemon config for a tool", true},
	{"service list", "lezz service list", "List all installed lezz services", true},
	{"service purge", "lezz service purge", "Unload and remove all lezz services", true},
	{"agent", "lezz agent setup|remove|show|build", "Wire an ACP agent into an editor's settings", false},
	{"agent setup", "lezz agent setup [editor]", "Wire an ACP agent into an editor's settings", true},
	{"agent build", "lezz agent build [--src DIR]", "Fetch/build workspace-agent into ~/.lezz/bin", true},
	{"agent remove", "lezz agent remove [editor]", "Remove it (requires --yes)", true},
	{"agent show", "lezz agent show [editor]", "Show the wired agent", true},
	{"version", "lezz version", "Print version", true},
}

// usageWidth aligns the synopsis column to the longest displayed invocation.
var usageWidth = func() int {
	w := 0
	for _, d := range commandDocs {
		if d.listed && len(d.display) > w {
			w = len(d.display)
		}
	}
	return w
}()

// lookupCommand resolves a typed path to its doc: an exact match wins, then
// the longest parent whose path is a prefix of it ("app install" → "app").
func lookupCommand(path string) (commandDoc, bool) {
	path = strings.TrimSpace(path)
	var best commandDoc
	found := false
	for _, d := range commandDocs {
		if d.path == path {
			return d, true
		}
		if strings.HasPrefix(path, d.path+" ") && (!found || len(d.path) > len(best.path)) {
			best, found = d, true
		}
	}
	return best, found
}

// printCommandUsage writes usage for one command path: its synopsis, its
// invocation, and its flags when it has a flag set. Nothing here has a side
// effect — that is the point of handling help before any command body runs.
func printCommandUsage(w io.Writer, path string, fs *flag.FlagSet) {
	path = strings.TrimSpace(path)
	usageLine := "lezz " + path
	derived := true
	if doc, ok := lookupCommand(path); ok {
		_, _ = fmt.Fprintf(w, "%s — %s\n\n", usageLine, doc.synopsis)
		// A doc for this exact path knows its arguments; a parent's display
		// line names the sibling actions instead.
		if doc.path == path {
			usageLine = doc.display
			derived = false
		}
	}
	flags := 0
	if fs != nil {
		fs.VisitAll(func(*flag.Flag) { flags++ })
	}
	_, _ = fmt.Fprintf(w, "Usage:\n  %s", usageLine)
	if flags > 0 && derived {
		_, _ = fmt.Fprint(w, " [flags]")
	}
	_, _ = fmt.Fprintln(w)
	if flags > 0 {
		out := fs.Output()
		fs.SetOutput(w)
		_, _ = fmt.Fprintln(w, "\nFlags:")
		fs.PrintDefaults()
		fs.SetOutput(out)
	}
	_, _ = fmt.Fprintln(w, "\nRun 'lezz --help' for the full command list.")
}

func usage() {
	printUsage(os.Stdout)
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, "lezz — self-updating host for adhd, ocd-smoke-alarm, and tuner\n\nUsage:\n")
	for _, d := range commandDocs {
		if !d.listed {
			continue
		}
		_, _ = fmt.Fprintf(w, "  %-*s  %s\n", usageWidth, d.display, d.synopsis)
	}
	_, _ = fmt.Fprint(w, `
Managed tools: lezz, adhd, ocd-smoke-alarm, tuner

Every command accepts -h/--help and prints its usage instead of running:
  lezz <command> --help
`)
}
