package demo

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/template"
	"time"

	"github.com/james-gibson/lezz.go/internal/tools"
)

// baseSmokeAlarmConfigTmpl configures one ocd-smoke-alarm instance in a base
// cluster. Unlike the "lezz demo" config, it hardcodes no targets: the
// instance starts empty and is populated at runtime by the server's
// auto-refresh (discovery) and remote-update (remote_agent) paths.
var baseSmokeAlarmConfigTmpl = template.Must(template.New("base-smoke-alarm").Parse(`version: "1"
service:
  name: "ocd-smoke-alarm"
  environment: "lezz-base"
  mode: "background"
  log_level: "warn"
  poll_interval: "5s"
  timeout: "3s"
  max_workers: 4

health:
  enabled: true
  listen_addr: "{{.ListenAddr}}:{{.Port}}"
  self_check: true
  endpoints:
    healthz: "/healthz"
    readyz: "/readyz"
    status: "/status"

runtime:
  lock_file: "{{.StateDir}}/lock"
  state_dir: "{{.StateDir}}"
  baseline_file: "{{.StateDir}}/known-good.json"
  event_history_size: 100
  graceful_shutdown_timeout: "5s"

discovery:
  enabled: true
  interval: "30s"
  include_local_paths:
    - "~/.config/mcp"
    - "~/.config/acp"
  local_proxy_scan:
    enabled: false
  cloud_catalog:
    enabled: false
    urls: []
  llms_txt:
    enabled: false

tuner:
  enabled: true
  advertise: true

alerts:
  aggressive: false
  notify_on_regression_immediately: true
  sinks:
    log:
      enabled: true

# Base cluster: empty and awaiting. Targets are registered at runtime via
# discovery (auto-refresh) or pushed via remote management — never hardcoded.
targets: []

# NOTE: auto is meant to chain instances link-by-link using smoke-alarm's
# federation slot election (the first instance to bind base_port becomes the
# introducer, each later instance claims the next slot). That is intentionally
# NOT enabled yet: the introducer currently deadlocks on the first introduction
# (james-gibson/smoke-alarm#10), which would wedge the whole base as soon as a
# second link joined. Re-add a federation block here once that is fixed.

dynamic_config:
  enabled: true
  directory: "{{.StateDir}}/dynamic-config"
  formats: ["json", "markdown"]
  serve_base_url: "/dynamic-config"
  allow_overwrite: true
  require_unique_ids: true

remote_agent:
  managed_updates: true
  update:
    strategy: "rolling-single"
    stop_command: "{{.Binary}} ops stop -pid-file {{.StateDir}}/ocd-smoke-alarm.pid"
    start_command: "nohup {{.Binary}} serve -config {{.ConfigPath}} >> {{.LogPath}} 2>&1 & sleep 1"
    verify_command: "{{.Binary}} check -config {{.ConfigPath}}"
    rollback_on_failure: false
    max_wait_for_healthy: "30s"
  safety:
    require_lock: false
`))

// baseSmokeAlarmConfig holds template data for one base smoke-alarm instance.
type baseSmokeAlarmConfig struct {
	Port       int
	ListenAddr string
	StateDir   string
	Binary     string
	ConfigPath string
	LogPath    string
}

// baseClusterName identifies a base cluster within the discovery registry.
func baseClusterName() string {
	return fmt.Sprintf("base-%d", os.Getpid())
}

// ensureTools installs any of the named managed tools that are missing from
// ~/.lezz/bin and PATH. It is the "auto" in "lezz auto": a base cluster should
// not fail because a managed binary has not been installed yet.
func ensureTools(ctx context.Context, names ...string) error {
	for _, name := range names {
		if _, err := tools.Find(name); err == nil {
			continue
		}
		tool, ok := tools.Lookup(name)
		if !ok {
			return fmt.Errorf("unknown managed tool %q", name)
		}
		fmt.Printf("installing missing tool %s ...\n", name)
		ver, err := tools.Install(ctx, tool)
		if err != nil {
			return fmt.Errorf("install %s: %w", name, err)
		}
		fmt.Printf("installed %s %s\n", name, ver)
	}
	return nil
}

// existingRegistry returns the clusters registered on the local discovery port,
// or ok=false when no registry is reachable.
func existingRegistry(ctx context.Context) (clusters []ClusterInfo, ok bool) {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	clusters, err := fetchAllClusters(reqCtx, fmt.Sprintf("http://localhost:%d/cluster", DiscoveryPort))
	if err != nil {
		return nil, false
	}
	return clusters, true
}

// printRegistry renders the clusters already advertised by a live registry.
func printRegistry(clusters []ClusterInfo) {
	if len(clusters) == 0 {
		fmt.Println("  (registry is up but has no clusters yet)")
		return
	}
	for _, c := range clusters {
		fmt.Printf("  %s\n", c.Name)
		fmt.Printf("    alarm-a  %s\n", c.AlarmA)
		fmt.Printf("    alarm-b  %s\n", c.AlarmB)
		fmt.Printf("    adhd     %s\n", c.AdhdMCP)
	}
}

// RunAuto launches a base cluster in "auto" mode and blocks until Ctrl+C or the
// context is canceled.
//
// Unlike Run, a base cluster is empty and awaiting: the two ocd-smoke-alarm
// instances carry no hardcoded targets and are instead populated at runtime by
// discovery (auto-refresh) and remote management (remote updates). adhd is
// started with --demo, so it builds its endpoints from the registry rather than
// a hardcoded config file.
//
// Auto mode also:
//   - installs any missing managed tools (adhd, ocd-smoke-alarm), and
//   - reuses an existing discovery registry instead of starting a competing
//     cluster on the same host.
func RunAuto(ctx context.Context) error {
	// Auto-detect: a live registry already owns the fixed discovery port and
	// the shared dashboard config, so reuse it rather than launching a second,
	// competing cluster.
	if clusters, ok := existingRegistry(ctx); ok {
		fmt.Println("a lezz discovery registry is already running — reusing it")
		printRegistry(clusters)
		fmt.Println("\nconnect a dashboard with:  adhd --demo")
		return nil
	}

	// Auto-install: the base cluster should not fail because a managed binary
	// has not been installed yet.
	if err := ensureTools(ctx, "adhd", "ocd-smoke-alarm"); err != nil {
		return err
	}

	// --- Pre-allocate ports -------------------------------------------------
	portA, err := freePort()
	if err != nil {
		return fmt.Errorf("pre-allocate port for alarm-a: %w", err)
	}
	portB, err := freePort()
	if err != nil {
		return fmt.Errorf("pre-allocate port for alarm-b: %w", err)
	}
	adhdPort, err := freePort()
	if err != nil {
		return fmt.Errorf("pre-allocate port for adhd MCP: %w", err)
	}

	// --- Temp directories ---------------------------------------------------
	tmpRoot, err := os.MkdirTemp("", "lezz-auto-*")
	if err != nil {
		return fmt.Errorf("create temp root: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tmpRoot); removeErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove temp dir %s: %v\n", tmpRoot, removeErr)
		}
	}()

	stateA := tmpRoot + "/state-a"
	stateB := tmpRoot + "/state-b"
	for _, d := range []string{stateA, stateB} {
		if mkErr := os.MkdirAll(d, 0o700); mkErr != nil {
			return fmt.Errorf("create state dir %s: %w", d, mkErr)
		}
	}

	alarmALogPath := tmpRoot + "/alarm-a.log"
	alarmBLogPath := tmpRoot + "/alarm-b.log"
	adhdLogPath := tmpRoot + "/adhd.log"

	alarmBin, err := tools.Find("ocd-smoke-alarm")
	if err != nil {
		return fmt.Errorf("resolve ocd-smoke-alarm: %w", err)
	}

	// --- Write empty, awaiting configs --------------------------------------
	configA, err := writeTempConfig(tmpRoot, "alarm-a", baseSmokeAlarmConfigTmpl, baseSmokeAlarmConfig{
		Port:       portA,
		ListenAddr: healthListenAddr,
		StateDir:   stateA,
		Binary:     alarmBin,
		ConfigPath: tmpRoot + "/alarm-a.yaml",
		LogPath:    alarmALogPath,
	})
	if err != nil {
		return err
	}

	configB, err := writeTempConfig(tmpRoot, "alarm-b", baseSmokeAlarmConfigTmpl, baseSmokeAlarmConfig{
		Port:       portB,
		ListenAddr: healthListenAddr,
		StateDir:   stateB,
		Binary:     alarmBin,
		ConfigPath: tmpRoot + "/alarm-b.yaml",
		LogPath:    alarmBLogPath,
	})
	if err != nil {
		return err
	}

	// --- Start ocd-smoke-alarm instances ------------------------------------
	cmdA, err := startProcess("ocd-smoke-alarm", []string{"serve", "-config", configA}, alarmALogPath)
	if err != nil {
		return fmt.Errorf("start alarm-a: %w", err)
	}
	defer killProcess(cmdA)

	cmdB, err := startProcess("ocd-smoke-alarm", []string{"serve", "-config", configB}, alarmBLogPath)
	if err != nil {
		return fmt.Errorf("start alarm-b: %w", err)
	}
	defer killProcess(cmdB)

	// --- Poll alarm readiness -----------------------------------------------
	fmt.Println("waiting for alarm-a to become ready...")
	if readyErr := waitReady(ctx, fmt.Sprintf("http://127.0.0.1:%d/healthz", portA)); readyErr != nil {
		return fmt.Errorf("alarm-a readiness: %w", readyErr)
	}
	fmt.Println("waiting for alarm-b to become ready...")
	if readyErr := waitReady(ctx, fmt.Sprintf("http://127.0.0.1:%d/healthz", portB)); readyErr != nil {
		return fmt.Errorf("alarm-b readiness: %w", readyErr)
	}

	host := outboundIP()
	clusterInfo := ClusterInfo{
		Name:     baseClusterName(),
		AlarmA:   fmt.Sprintf("http://%s:%d", host, portA),
		AlarmB:   fmt.Sprintf("http://%s:%d", host, portB),
		AdhdMCP:  fmt.Sprintf("http://%s:%d/mcp", host, adhdPort),
		Projects: projectSeedsFromEnv(),
	}

	// --- Start discovery BEFORE adhd so `adhd --demo` can find the base -----
	discoverySrv, discoveryErr := startDiscoveryServer(clusterInfo)
	switch discoveryErr {
	case nil:
		// We own the port — also register mDNS so LAN clients can find us.
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer shutdownCancel()
			_ = discoverySrv.Shutdown(shutdownCtx)
		}()
		if mdnsSrv, mdnsErr := registerMDNS(); mdnsErr != nil {
			fmt.Fprintf(os.Stderr, "warning: mDNS registration failed: %v\n", mdnsErr)
		} else {
			defer mdnsSrv.Shutdown()
		}
	default:
		// Lost a race for the port — join the registry that won instead.
		deregFn, joinErr := joinDiscoveryServer(clusterInfo)
		if joinErr != nil {
			fmt.Fprintf(os.Stderr, "warning: discovery unavailable (port busy, not a lezz registry): %v\n", joinErr)
		} else {
			fmt.Printf("joined existing discovery registry as %s\n", clusterInfo.Name)
			defer deregFn()
		}
	}

	// --- Start adhd headless in demo-discovery mode -------------------------
	// No hardcoded config: adhd builds its smoke_alarm endpoints from whatever
	// the registry advertises, and keeps polling for clusters that join later.
	smokeAlarmURL := fmt.Sprintf("http://0.0.0.0:%d", portA)
	cmdADHD, err := startProcess("adhd", []string{
		"--headless",
		"--demo",
		"--log", adhdLogPath,
		"--mcp-addr", fmt.Sprintf(":%d", adhdPort),
		"--smoke-alarm", smokeAlarmURL,
	}, adhdLogPath)
	if err != nil {
		return fmt.Errorf("start adhd: %w", err)
	}
	defer killProcess(cmdADHD)

	// --- Poll adhd MCP readiness --------------------------------------------
	// GET /mcp is an SSE endpoint that never closes, so HTTP-based readiness
	// polling always times out. A TCP dial on the port is sufficient.
	fmt.Println("waiting for adhd MCP to become ready...")
	if readyErr := waitPortOpen(ctx, fmt.Sprintf("0.0.0.0:%d", adhdPort)); readyErr != nil {
		return fmt.Errorf("adhd MCP readiness: %w", readyErr)
	}

	// Tell our own ADHD about this cluster so it registers the cluster peer and
	// any project proxy tools without waiting for another cluster to join.
	sendJoin(context.Background(), clusterInfo.AdhdMCP, clusterInfo)

	if discoveryErr != nil {
		// We joined someone else's registry — let the clusters already there
		// know about the base so their dashboards update immediately.
		notifyExistingADHD(clusterInfo)
	}

	// --- Query isotope trust level from alarm-a -----------------------------
	trustSummary := queryIsotopeTrust(ctx, smokeAlarmURL)

	// --- Print summary ------------------------------------------------------
	fmt.Printf(`
lezz auto base cluster ready (empty, awaiting config)

alarm-a      %s/status   (no targets — awaiting auto/remote config)
alarm-b      %s/status   (no targets — awaiting auto/remote config)
adhd MCP     %s  (config discovered from the registry)
isotopes     %s
registry     http://localhost:%d/cluster

connect dashboard:  adhd --demo

Targets are picked up automatically as local MCP/ACP configs appear, or pushed
via remote updates. Logs:

  %s
  %s
  %s

Ctrl+C to stop
`, clusterInfo.AlarmA, clusterInfo.AlarmB, clusterInfo.AdhdMCP, trustSummary, DiscoveryPort,
		alarmALogPath, alarmBLogPath, adhdLogPath)

	// --- Wait for shutdown signal -------------------------------------------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
		fmt.Println("\nshutting down base cluster...")
	case <-ctx.Done():
		fmt.Println("\ncontext canceled, shutting down base cluster...")
	}

	return nil
}
