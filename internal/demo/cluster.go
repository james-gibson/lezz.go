// Package demo implements the "lezz demo" self-contained cluster launcher.
package demo

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"text/template"
	"time"

	"github.com/james-gibson/lezz.go/internal/tools"
)

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

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
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
  mode: "foreground"
  log_level: "warn"
  poll_interval: "5s"
  timeout: "3s"
  max_workers: 4

health:
  enabled: true
  listen_addr: "127.0.0.1:{{.Port}}"
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
	Port     int
	StateDir string
	PeerName string
	PeerPort int
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

// killProcess sends SIGTERM to a process if it is still running.
func killProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
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
		Port:     portA,
		StateDir: stateA,
		PeerName: "alarm-b",
		PeerPort: portB,
	})
	if err != nil {
		return err
	}

	configB, err := writeTempConfig(tmpRoot, "alarm-b", smokeAlarmConfigTmpl, smokeAlarmConfig{
		Port:     portB,
		StateDir: stateB,
		PeerName: "alarm-a",
		PeerPort: portA,
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
	if err := copyFile(adhdConfigPath, stableConfigPath); err != nil {
		return fmt.Errorf("write stable adhd config: %w", err)
	}
	defer func() { _ = os.Remove(stableConfigPath) }()

	adhdLogPath := tmpRoot + "/adhd.log"

	// --- Start ocd-smoke-alarm instances ------------------------------------
	cmdA, err := tools.Start("ocd-smoke-alarm", []string{"--config", configA})
	if err != nil {
		return fmt.Errorf("start alarm-a: %w", err)
	}
	defer killProcess(cmdA)

	cmdB, err := tools.Start("ocd-smoke-alarm", []string{"--config", configB})
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
	cmdADHD, err := tools.Start("adhd", []string{
		"--headless",
		"--config", adhdConfigPath,
		"--log", adhdLogPath,
		"--mcp-addr", fmt.Sprintf(":%d", adhdPort),
	})
	if err != nil {
		return fmt.Errorf("start adhd: %w", err)
	}
	defer killProcess(cmdADHD)

	// --- Poll adhd MCP readiness --------------------------------------------
	fmt.Println("waiting for adhd MCP to become ready...")
	if readyErr := waitReady(ctx, fmt.Sprintf("http://127.0.0.1:%d/mcp", adhdPort)); readyErr != nil {
		return fmt.Errorf("adhd MCP readiness: %w", readyErr)
	}

	// --- Print summary ------------------------------------------------------
	fmt.Printf(`
lezz demo cluster ready

alarm-a   http://127.0.0.1:%d/status
alarm-b   http://127.0.0.1:%d/status
adhd MCP  http://127.0.0.1:%d/mcp

connect dashboard:  adhd --config %s

Ctrl+C to stop
`, portA, portB, adhdPort, stableConfigPath)

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
