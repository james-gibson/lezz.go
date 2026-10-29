package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Options pins the demo cluster's listener ports. A zero field leaves the
// choice to lezz: the port the last run used when it is still free, otherwise a
// fresh allocation. Pinning makes the endpoints in saved MCP configs survive a
// restart.
type Options struct {
	AlarmAPort int
	AlarmBPort int
	ADHDPort   int

	// ADHDBind is the host the adhd MCP surface listens on. Empty keeps the
	// demo's LAN default (all interfaces, advertised at the outbound IP). A
	// loopback host binds only loopback and advertises that, so a cluster
	// fronted by a local gate does not leave the raw adhd reachable on the LAN
	// as a bypass (lezz.go#58). The alarms keep their all-interfaces bind
	// either way: their /status is read-only.
	ADHDBind string

	// RegistryBind is the host the /cluster discovery registry listens on.
	// Empty is loopback (RegistryBindDefault): the registry is an
	// unauthenticated read-and-write directory, so the default keeps it off the
	// LAN, where remote `lezz see` and `adhd --demo` cannot enumerate it or
	// register into it (lezz.go#59). An unspecified host (0.0.0.0, ::) or a
	// specific LAN IP publishes it — and only then is it advertised over mDNS.
	RegistryBind string
}

// Ports is the listener set a demo cluster owns.
type Ports struct {
	AlarmA  int       `json:"alarm_a"`
	AlarmB  int       `json:"alarm_b"`
	ADHD    int       `json:"adhd"`
	SavedAt time.Time `json:"saved_at"`
}

// State is the live record of a running demo cluster. The process that owns the
// cluster writes it once the cluster is ready and removes it on shutdown, so
// `lezz demo status|stop` can reach a detached run without guessing.
type State struct {
	PID          int         `json:"pid"`
	StartedAt    time.Time   `json:"started_at"`
	Cluster      ClusterInfo `json:"cluster"`
	Ports        Ports       `json:"ports"`
	Discovery    string      `json:"discovery,omitempty"`
	StableConfig string      `json:"stable_config,omitempty"`
	LogDir       string      `json:"log_dir,omitempty"`
}

// Health is one probed endpoint of a running cluster.
type Health struct {
	Name     string
	Endpoint string
	OK       bool
	Detail   string
}

// lezzDir returns ~/.lezz, creating it.
func lezzDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".lezz")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create .lezz dir: %w", err)
	}
	return dir, nil
}

func portsFile() (string, error) {
	dir, err := lezzDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "demo-ports.json"), nil
}

func stateFile() (string, error) {
	dir, err := lezzDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "demo-state.json"), nil
}

// LogPath is where `lezz demo start` sends the detached child's output.
func LogPath() (string, error) {
	dir, err := lezzDir()
	if err != nil {
		return "", err
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return "", fmt.Errorf("create log dir: %w", err)
	}
	return filepath.Join(logDir, "demo.log"), nil
}

// loadPorts returns the ports the last run used, if any.
func loadPorts() (Ports, bool) {
	path, err := portsFile()
	if err != nil {
		return Ports{}, false
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is ~/.lezz/demo-ports.json
	if err != nil {
		return Ports{}, false
	}
	var p Ports
	if json.Unmarshal(data, &p) != nil {
		return Ports{}, false
	}
	return p, true
}

func savePorts(p Ports) error {
	path, err := portsFile()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encode demo ports: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o600) //nolint:gosec // ~/.lezz, 0600 by design
}

// portFree reports whether a listener can bind port on loopback.
func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// validatePins fails fast when a pinned port is taken or two pins collide.
func validatePins(opts Options) error {
	if err := validateADHDBind(opts.ADHDBind); err != nil {
		return err
	}
	pins := []struct {
		label string
		port  int
	}{
		{"alarm-a", opts.AlarmAPort},
		{"alarm-b", opts.AlarmBPort},
		{"adhd", opts.ADHDPort},
	}
	seen := make(map[int]string, len(pins))
	for _, p := range pins {
		if p.port <= 0 {
			continue
		}
		if other, dup := seen[p.port]; dup {
			return fmt.Errorf("%s port %d is already pinned for %s", p.label, p.port, other)
		}
		if !portFree(p.port) {
			return fmt.Errorf("%s port %d is already in use; stop the process holding it or pass a different port", p.label, p.port)
		}
		seen[p.port] = p.label
	}
	return nil
}

// resolvePorts picks the three listener ports. Pinned ports win and fail closed
// when they are taken; otherwise the previous run's port is reused when free,
// so restarts keep the same endpoints.
func resolvePorts(opts Options) (Ports, error) {
	previous, _ := loadPorts()
	used := make(map[int]bool, 3)

	take := func(label, flagName string, pinned, last int) (int, error) {
		if pinned > 0 {
			if used[pinned] {
				return 0, fmt.Errorf("%s port %d is already pinned for another listener", label, pinned)
			}
			if !portFree(pinned) {
				return 0, fmt.Errorf("%s port %d is already in use; stop the process holding it or pass a different --%s", label, pinned, flagName)
			}
			used[pinned] = true
			return pinned, nil
		}
		if last > 0 && !used[last] && portFree(last) {
			used[last] = true
			return last, nil
		}
		for i := 0; i < 20; i++ {
			port, err := freePort()
			if err != nil {
				return 0, err
			}
			if used[port] {
				continue
			}
			used[port] = true
			return port, nil
		}
		return 0, fmt.Errorf("could not allocate a distinct port for %s", label)
	}

	alarmA, err := take("alarm-a", "alarm-a-port", opts.AlarmAPort, previous.AlarmA)
	if err != nil {
		return Ports{}, err
	}
	alarmB, err := take("alarm-b", "alarm-b-port", opts.AlarmBPort, previous.AlarmB)
	if err != nil {
		return Ports{}, err
	}
	adhd, err := take("adhd", "adhd-port", opts.ADHDPort, previous.ADHD)
	if err != nil {
		return Ports{}, err
	}
	return Ports{AlarmA: alarmA, AlarmB: alarmB, ADHD: adhd, SavedAt: time.Now().UTC()}, nil
}

// writeState publishes the running cluster.
func writeState(s State) error {
	path, err := stateFile()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode demo state: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o600) //nolint:gosec // ~/.lezz, 0600 by design
}

func readState() (State, bool) {
	path, err := stateFile()
	if err != nil {
		return State{}, false
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is ~/.lezz/demo-state.json
	if err != nil {
		return State{}, false
	}
	var s State
	if json.Unmarshal(data, &s) != nil || s.PID <= 0 {
		return State{}, false
	}
	return s, true
}

// RunningState returns the state of a live demo cluster. A state file whose
// process is gone is stale and is removed.
func RunningState() (State, bool) {
	s, ok := readState()
	if !ok {
		return State{}, false
	}
	if !processAlive(s.PID) {
		removeState(s.PID)
		return State{}, false
	}
	return s, true
}

// removeState deletes the state file only when it still describes pid, so a
// finishing cluster cannot delete its successor's state.
func removeState(pid int) {
	s, ok := readState()
	if !ok || s.PID != pid {
		return
	}
	path, err := stateFile()
	if err != nil {
		return
	}
	_ = os.Remove(path)
}

// StartDetached launches `lezz demo` as a detached child and waits until it has
// published its state, so the caller can print real endpoints instead of a
// guess. The child's output goes to ~/.lezz/logs/demo.log.
func StartDetached(ctx context.Context, opts Options) (State, error) {
	if running, ok := RunningState(); ok {
		return State{}, fmt.Errorf("a demo cluster is already running (pid %d); lezz demo status", running.PID)
	}
	// Report a pinned-port conflict here rather than leaving the caller to read
	// the child's log.
	if err := validatePins(opts); err != nil {
		return State{}, err
	}
	// Same reason for the binds: an unparseable --registry-bind must fail here,
	// where the operator can see it, not in the detached child's log.
	if _, _, err := registryHost(opts.RegistryBind, ""); err != nil {
		return State{}, err
	}

	exe, err := os.Executable()
	if err != nil {
		return State{}, fmt.Errorf("locate executable: %w", err)
	}
	logPath, err := LogPath()
	if err != nil {
		return State{}, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // ~/.lezz/logs/demo.log
	if err != nil {
		return State{}, fmt.Errorf("open demo log %s: %w", logPath, err)
	}

	args := []string{"demo"}
	if opts.AlarmAPort > 0 {
		args = append(args, fmt.Sprintf("--alarm-a-port=%d", opts.AlarmAPort))
	}
	if opts.AlarmBPort > 0 {
		args = append(args, fmt.Sprintf("--alarm-b-port=%d", opts.AlarmBPort))
	}
	if opts.ADHDPort > 0 {
		args = append(args, fmt.Sprintf("--adhd-port=%d", opts.ADHDPort))
	}
	if opts.ADHDBind != "" {
		args = append(args, "--adhd-bind="+opts.ADHDBind)
	}
	if opts.RegistryBind != "" {
		args = append(args, "--registry-bind="+opts.RegistryBind)
	}

	cmd := exec.Command(exe, args...) //nolint:gosec // exe is this binary, resolved via os.Executable
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return State{}, fmt.Errorf("start detached demo: %w", err)
	}
	pid := cmd.Process.Pid
	// The child holds its own descriptor to the log file.
	_ = logFile.Close()

	// Wait for the child in the background purely to learn whether it died: a
	// released, exited child would linger as a zombie and look alive. If this
	// process exits first, the detached child keeps running (its own session).
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case waitErr := <-exited:
			return State{}, fmt.Errorf("demo exited during startup (%w); last output from %s:\n%s", waitErr, logPath, tailFile(logPath, 20))
		default:
		}
		if state, ok := readState(); ok && state.PID == pid {
			return state, nil
		}
		if time.Now().After(deadline) {
			return State{}, fmt.Errorf("timed out waiting for the demo cluster to become ready; see %s", logPath)
		}
		select {
		case <-ctx.Done():
			return State{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Stop terminates the running cluster and waits for it to exit. It returns nil
// when nothing is running.
func Stop(timeout time.Duration) error {
	state, ok := RunningState()
	if !ok {
		return nil
	}
	if !looksLikeDemo(state.PID) {
		return fmt.Errorf("refusing to signal pid %d: it does not look like a lezz demo process", state.PID)
	}

	proc, err := os.FindProcess(state.PID)
	if err != nil {
		return fmt.Errorf("find demo process %d: %w", state.PID, err)
	}
	if err := proc.Signal(interruptSignal()); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal demo process %d: %w", state.PID, err)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(state.PID) {
			removeState(state.PID)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("demo (pid %d) did not exit within %s", state.PID, timeout)
}

// Probe checks the cluster's endpoints. It is best-effort: a failing endpoint
// is reported, never returned as an error.
func Probe(ctx context.Context, state State) []Health {
	client := &http.Client{Timeout: 2 * time.Second}

	alarm := func(name, base string) Health {
		h := Health{Name: name, Endpoint: base}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/healthz", http.NoBody)
		if err != nil {
			h.Detail = err.Error()
			return h
		}
		resp, err := client.Do(req)
		if err != nil {
			h.Detail = "unreachable"
			return h
		}
		defer func() { _ = resp.Body.Close() }()
		h.OK = resp.StatusCode == http.StatusOK
		h.Detail = resp.Status
		return h
	}

	health := []Health{alarm("alarm-a", state.Cluster.AlarmA), alarm("alarm-b", state.Cluster.AlarmB)}

	h := Health{Name: "adhd MCP", Endpoint: state.Cluster.AdhdMCP}
	if host := hostOf(state.Cluster.AdhdMCP); host != "" {
		conn, err := net.DialTimeout("tcp", host, 2*time.Second)
		if err != nil {
			h.Detail = "unreachable"
		} else {
			_ = conn.Close()
			h.OK = true
			h.Detail = "listening"
		}
	} else {
		h.Detail = "no endpoint"
	}
	health = append(health, h)

	h = Health{Name: "discovery", Endpoint: state.Discovery}
	if state.Discovery != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, state.Discovery, http.NoBody)
		if err == nil {
			resp, reqErr := client.Do(req)
			if reqErr != nil {
				h.Detail = "unreachable"
			} else {
				_ = resp.Body.Close()
				h.OK = resp.StatusCode == http.StatusOK
				h.Detail = resp.Status
			}
		}
	} else {
		h.Detail = "no endpoint"
	}
	return append(health, h)
}

// hostOf extracts host:port from an absolute URL.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}
