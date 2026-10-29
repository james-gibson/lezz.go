package demo

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"text/template"
	"time"

	"github.com/james-gibson/smoke-alarm/pkg/smokealarm"
)

// linkConfigTmpl is the in-process equivalent of the YAML lezz demo used to
// write to disk for each alarm. It is rendered in memory and parsed with
// smokealarm.ParseConfig: the facade aliases Config but not its nested types
// (TargetConfig and friends are internal), so a target list cannot be built
// with Go composite literals from outside the module.
//
// Differences from the config the subprocess used to get:
//
//   - no remote_agent.managed_updates — it describes start/stop commands for a
//     *binary*, which has no meaning for an in-process link (the facade reports
//     it as unsupported, and Options.Strict below turns that into an error);
//   - no runtime.lock_file — an embedded instance deliberately writes no lock or
//     PID file, because writing lezz's PID would make `ops stop -pid-file`
//     signal lezz itself;
//   - known_state is enabled and persists, so each link actually establishes a
//     baseline (state-a / state-b keep the two files from colliding).
var linkConfigTmpl = template.Must(template.New("demo-link").Parse(`version: "1"
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
  state_dir: "{{.StateDir}}"
  baseline_file: "{{.StateDir}}/known-good.json"
  event_history_size: 100
  graceful_shutdown_timeout: "5s"

known_state:
  enabled: true
  persist: true

discovery:
  enabled: false

tuner:
  enabled: true
  advertise: {{.Advertise}}
  service_type: "_smoke-alarm._tcp"

alerts:
  aggressive: false
  notify_on_regression_immediately: true
  sinks:
    log:
      enabled: true

{{.TrustYAML}}
targets:
  - id: "peer"
    enabled: true
    protocol: "http"
    name: "{{.PeerName}}"
    endpoint: "http://localhost:{{.PeerPort}}/status"
    transport: "http"
    expected:
      healthy_status_codes: [200]
`))

// linkParams is the template data for one in-process alarm.
type linkParams struct {
	Port       int    // requested health port; a hint, see startLink
	ListenAddr string // bind host, e.g. "0.0.0.0" or "127.0.0.1"
	StateDir   string // per-link state dir; holds that link's known-state baseline
	PeerName   string
	PeerPort   int
	Advertise  bool   // mDNS advertisement for tuner
	TrustYAML  string // trust block: the cluster secret this link verifies with
}

// buildLinkConfig renders and validates one link's config.
func buildLinkConfig(p linkParams) (smokealarm.Config, error) {
	var buf bytes.Buffer
	if err := linkConfigTmpl.Execute(&buf, p); err != nil {
		return smokealarm.Config{}, fmt.Errorf("render alarm config: %w", err)
	}
	cfg, err := smokealarm.ParseConfig(buf.Bytes())
	if err != nil {
		return smokealarm.Config{}, fmt.Errorf("parse alarm config: %w", err)
	}
	return cfg, nil
}

// inProcessLink is one smoke-alarm running inside lezz.
type inProcessLink struct {
	name string
	inst *smokealarm.Instance
	log  *os.File
}

// startLink builds the config, wires a per-link logger to logPath (so the
// existing ~/.lezz log artifacts keep working and the link's log lines do not
// interleave with lezz's stdout), and starts the instance.
//
// Options.Strict is set deliberately: the config is ours, so a link that would
// enable a subsystem the facade cannot run (federation, hosted validation,
// discovery) must fail here rather than start half-inert.
func startLink(ctx context.Context, name string, p linkParams, logPath string) (*inProcessLink, error) {
	cfg, err := buildLinkConfig(p)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // logPath is constructed from a temp dir under our control
	if err != nil {
		return nil, fmt.Errorf("%s: open log file %s: %w", name, logPath, err)
	}

	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slogLevel(cfg.Service.LogLevel)}))
	inst, err := smokealarm.New(cfg, smokealarm.Options{
		Version: "lezz-demo",
		Logger:  logger,
		Strict:  true,
	})
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := inst.Start(ctx); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%s: start: %w", name, err)
	}
	return &inProcessLink{name: name, inst: inst, log: f}, nil
}

// addr is the address the health listener actually bound.
func (l *inProcessLink) addr() string {
	if l == nil || l.inst == nil {
		return ""
	}
	return l.inst.Addr()
}

// port is the port the health listener actually bound.
func (l *inProcessLink) port() (int, error) {
	return portOf(l.addr())
}

// healthURL is the loopback readiness URL. The listener may be bound to
// 0.0.0.0, which is not a useful destination for a client.
func (l *inProcessLink) healthURL() string {
	p, err := l.port()
	if err != nil {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/healthz", p)
}

// shutdown stops the link and closes its log file. Safe to call more than once.
func (l *inProcessLink) shutdown() {
	if l == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := l.inst.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s shutdown: %v\n", l.name, err)
	}
	if l.log != nil {
		_ = l.log.Close()
		l.log = nil
	}
}

// checkLinkPorts verifies that each link bound the port its peer's target
// names. startLink passes the pre-allocated port as a hint; the facade retries
// on a contended port, so a mismatch is possible and must not pass silently.
func checkLinkPorts(a *inProcessLink, wantA int, b *inProcessLink, wantB int) error {
	for _, c := range []struct {
		link *inProcessLink
		want int
	}{{a, wantA}, {b, wantB}} {
		got, err := c.link.port()
		if err != nil {
			return fmt.Errorf("%s: %w", c.link.name, err)
		}
		if got != c.want {
			return fmt.Errorf(
				"%s bound port %d instead of the pre-allocated %d (the port was taken in the meantime), which would leave the peers pointed at nothing; re-run lezz demo",
				c.link.name, got, c.want)
		}
	}
	return nil
}

// portOf splits a host:port address into its numeric port.
func portOf(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("split address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("parse port in %q: %w", addr, err)
	}
	return port, nil
}

// slogLevel maps smoke-alarm's configured log level onto slog.
func slogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}
