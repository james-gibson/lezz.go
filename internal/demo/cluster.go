// Package demo implements the "lezz demo" self-contained cluster launcher.
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"text/template"
	"time"

	"github.com/james-gibson/isotope"
	"github.com/james-gibson/lezz.go/internal/tools"
)

// startProcess finds a managed tool binary and starts it with stdout/stderr
// redirected to logFile. Unlike tools.Start, the terminal is not polluted by
// child process output (TUI escape codes, color sequences, etc.).
func startProcess(name string, args []string, logFile string) (*exec.Cmd, error) {
	binary, err := tools.Find(name)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // logFile is constructed from a temp dir under our control
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", logFile, err)
	}

	cmd := exec.Command(binary, args...) //nolint:gosec // binary path is resolved via tools.Find()
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	// Close our copy of the file descriptor — the child process holds its own.
	_ = f.Close()
	return cmd, nil
}

func clusterName() string {
	return fmt.Sprintf("demo-%d", os.Getpid())
}

// healthListenAddr is the bind address for smoke-alarm health servers.
// 0.0.0.0 makes them reachable from the LAN, not just localhost.
const healthListenAddr = "0.0.0.0"

// stableDemoConfigPath returns ~/.lezz/demo-adhd.yaml, creating the parent dir.
func stableDemoConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := home + "/.lezz"
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create .lezz dir: %w", err)
	}
	return dir + "/demo-adhd.yaml", nil
}

// copyFile copies src to dst, creating or truncating dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // src is a path we just wrote in a temp dir we control
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // dst is ~/.lezz/demo-adhd.yaml under our control
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if readErr != nil {
			if readErr.Error() == "EOF" {
				break
			}
			return readErr
		}
	}
	return nil
}

// freePort pre-allocates a free TCP port and returns it.
// The listener is closed immediately so the port can be reused.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate port: %w", err)
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		_ = l.Close()
		return 0, fmt.Errorf("unexpected listener address type")
	}
	port := addr.Port
	if err := l.Close(); err != nil {
		return 0, fmt.Errorf("close listener: %w", err)
	}
	return port, nil
}

// smokeAlarmConfigTmpl is the minimal YAML config for one ocd-smoke-alarm instance.
var smokeAlarmConfigTmpl = template.Must(template.New("smoke-alarm").Parse(`version: "1"
service:
  name: "ocd-smoke-alarm"
  environment: "lezz-demo"
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

targets:
  - id: "peer"
    enabled: true
    protocol: "http"
    name: "{{.PeerName}}"
    endpoint: "http://127.0.0.1:{{.PeerPort}}"
    transport: "http"
    expected:
      healthy_status_codes: [200]
`))

// smokeAlarmConfig holds template data for one smoke-alarm instance.
type smokeAlarmConfig struct {
	Port       int
	ListenAddr string
	StateDir   string
	PeerName   string
	PeerPort   int
}

// adhdConfigTmpl is the minimal YAML config for adhd in headless mode.
var adhdConfigTmpl = template.Must(template.New("adhd").Parse(`mcp_server:
  enabled: true
  addr: ":{{.ADHDPort}}"
smoke_alarm:
  - name: "alarm-a"
    endpoint: "http://127.0.0.1:{{.PortA}}"
    interval: "5s"
    use_sse: false
  - name: "alarm-b"
    endpoint: "http://127.0.0.1:{{.PortB}}"
    interval: "5s"
    use_sse: false
`))

// adhdConfig holds template data for the adhd config.
type adhdConfig struct {
	ADHDPort int
	PortA    int
	PortB    int
}

// writeTempConfig renders a template into a temp file and returns the path.
func writeTempConfig(dir, name string, tmpl *template.Template, data any) (string, error) {
	path := fmt.Sprintf("%s/%s.yaml", dir, name)
	f, err := os.Create(path) //nolint:gosec // path is constructed from an os.MkdirTemp directory under our control
	if err != nil {
		return "", fmt.Errorf("create config %s: %w", name, err)
	}
	if err := tmpl.Execute(f, data); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("render config %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close config %s: %w", name, err)
	}
	return path, nil
}

// waitReady polls url until it returns HTTP 200 or the context is done.
// Maximum wait is 15 seconds.
func waitReady(ctx context.Context, url string) error {
	deadline := time.Now().Add(15 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s to become ready", url)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := client.Get(url) //nolint:gosec // url is constructed from localhost addresses under our control
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// waitPortOpen dials addr (host:port) repeatedly until a TCP connection
// succeeds or the deadline is reached. Used for services whose HTTP endpoints
// are not suitable for readiness polling (e.g. SSE streams that never close).
func waitPortOpen(ctx context.Context, addr string) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s to open", addr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// killProcess sends SIGTERM to a process if it is still running.
func killProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
}

// queryIsotopeTrust polls the isotope list from the smoke-alarm health server
// until at least one entry appears or the deadline is reached. The poll exists
// because headless adhd registers itself after its MCP port opens, so the TCP
// readiness check does not guarantee the registration has arrived yet.
func queryIsotopeTrust(ctx context.Context, smokeAlarmURL string) string {
	deadline := time.Now().Add(5 * time.Second)
	client := isotope.NewClient(smokeAlarmURL)
	for {
		reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		entries, err := client.List(reqCtx)
		cancel()

		if err == nil && len(entries) > 0 {
			result := ""
			for _, e := range entries {
				if result != "" {
					result += ", "
				}
				result += fmt.Sprintf("%s rung %d (%s)", e.Name, e.TrustRung, e.RungName)
			}
			return result
		}

		if time.Now().After(deadline) {
			if err != nil {
				return "unavailable"
			}
			return "none registered"
		}
		select {
		case <-ctx.Done():
			return "unavailable"
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// notifyExistingADHD sends an adhd.cluster.join MCP call to all ADHD instances
// listed in the local discovery registry, informing them about this newly-joined
// cluster. Best-effort: individual failures are silently ignored.
func notifyExistingADHD(newCluster ClusterInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	existing, err := fetchAllClusters(ctx, fmt.Sprintf("http://127.0.0.1:%d/cluster", DiscoveryPort))
	if err != nil {
		return
	}

	payload, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "adhd.cluster.join",
		"params": map[string]interface{}{
			"name":    newCluster.Name,
			"alarm_a": newCluster.AlarmA,
			"alarm_b": newCluster.AlarmB,
		},
	})
	if err != nil {
		return
	}

	client := &http.Client{Timeout: 3 * time.Second}
	for _, c := range existing {
		if c.Name == newCluster.Name || c.AdhdMCP == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.AdhdMCP, bytes.NewReader(payload))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			fmt.Printf("notified ADHD at %s about cluster %s\n", c.AdhdMCP, newCluster.Name)
		}
	}
}

// Run launches a self-contained demo cluster and blocks until Ctrl+C or the
// context is canceled.
func Run(ctx context.Context) error {
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
	tmpRoot, err := os.MkdirTemp("", "lezz-demo-*")
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

	// --- Write configs ------------------------------------------------------
	configA, err := writeTempConfig(tmpRoot, "alarm-a", smokeAlarmConfigTmpl, smokeAlarmConfig{
		Port:       portA,
		ListenAddr: healthListenAddr,
		StateDir:   stateA,
		PeerName:   "alarm-b",
		PeerPort:   portB,
	})
	if err != nil {
		return err
	}

	configB, err := writeTempConfig(tmpRoot, "alarm-b", smokeAlarmConfigTmpl, smokeAlarmConfig{
		Port:       portB,
		ListenAddr: healthListenAddr,
		StateDir:   stateB,
		PeerName:   "alarm-a",
		PeerPort:   portA,
	})
	if err != nil {
		return err
	}

	adhdConfigPath, err := writeTempConfig(tmpRoot, "adhd", adhdConfigTmpl, adhdConfig{
		ADHDPort: adhdPort,
		PortA:    portA,
		PortB:    portB,
	})
	if err != nil {
		return err
	}

	// Also write to a stable path so the dashboard can always find the cluster.
	stableConfigPath, err := stableDemoConfigPath()
	if err != nil {
		return fmt.Errorf("resolve stable config path: %w", err)
	}
	copyErr := copyFile(adhdConfigPath, stableConfigPath)
	if copyErr != nil {
		return fmt.Errorf("write stable adhd config: %w", copyErr)
	}
	defer func() { _ = os.Remove(stableConfigPath) }()

	alarmALogPath := tmpRoot + "/alarm-a.log"
	alarmBLogPath := tmpRoot + "/alarm-b.log"
	adhdLogPath := tmpRoot + "/adhd.log"

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

	// --- Start adhd headless ------------------------------------------------
	smokeAlarmURL := fmt.Sprintf("http://127.0.0.1:%d", portA)
	cmdADHD, err := startProcess("adhd", []string{
		"--headless",
		"--config", adhdConfigPath,
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
	if readyErr := waitPortOpen(ctx, fmt.Sprintf("127.0.0.1:%d", adhdPort)); readyErr != nil {
		return fmt.Errorf("adhd MCP readiness: %w", readyErr)
	}

	// --- Start discovery (fixed port + mDNS) --------------------------------
	host := outboundIP()
	clusterInfo := ClusterInfo{
		Name:    clusterName(),
		AlarmA:  fmt.Sprintf("http://%s:%d", host, portA),
		AlarmB:  fmt.Sprintf("http://%s:%d", host, portB),
		AdhdMCP: fmt.Sprintf("http://%s:%d/mcp", host, adhdPort),
	}

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
		// Port taken — try to join an existing lezz registry.
		deregFn, joinErr := joinDiscoveryServer(clusterInfo)
		if joinErr != nil {
			fmt.Fprintf(os.Stderr, "warning: discovery unavailable (port busy, not a lezz registry): %v\n", joinErr)
		} else {
			fmt.Printf("joined existing discovery registry as %s\n", clusterInfo.Name)
			defer deregFn()
			// Immediately push our cluster info to all existing ADHD MCP instances
			// so their dashboards update without waiting for the next poll cycle.
			notifyExistingADHD(clusterInfo)
		}
	}

	// --- Query isotope trust level from alarm-a -----------------------------
	trustSummary := queryIsotopeTrust(ctx, smokeAlarmURL)

	// --- Print summary ------------------------------------------------------
	fmt.Printf(`
lezz demo cluster ready

alarm-a      %s/status
alarm-b      %s/status
adhd MCP     %s
isotopes     %s
discovery    http://%s:%d/cluster

connect dashboard:  adhd --config %s

logs         %s
             %s
             %s

Ctrl+C to stop
`, clusterInfo.AlarmA, clusterInfo.AlarmB, clusterInfo.AdhdMCP, trustSummary, host, DiscoveryPort, stableConfigPath,
		alarmALogPath, alarmBLogPath, adhdLogPath)

	// --- Wait for shutdown signal -------------------------------------------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
		fmt.Println("\nshutting down demo cluster...")
	case <-ctx.Done():
		fmt.Println("\ncontext canceled, shutting down demo cluster...")
	}

	return nil
}
