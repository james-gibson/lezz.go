package demo

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/james-gibson/lezz.go/internal/tools"
)

// adhdForce is adhd's advertise-guard override. The demo's default bind is all
// interfaces on purpose so the LAN can discover the cluster, and a guard-aware
// adhd refuses to advertise an unguarded MCP endpoint without it (adhd#15).
// A loopback bind does not need it and does not pass it (lezz.go#58).
const adhdForce = "--force"

// demoADHDArgs is the command line the demo starts adhd with. --force is only
// added for a bind that reaches beyond loopback: that bind is unguarded, and a
// guard-aware adhd refuses to advertise it without the override (adhd#15). A
// loopback bind needs no override because nothing off-host can reach it.
func demoADHDArgs(configPath, logPath string, listener adhdListener, smokeAlarmURL string) []string {
	args := []string{
		"--headless",
		"--config", configPath,
		"--log", logPath,
		"--mcp-addr", listener.Addr,
		"--smoke-alarm", smokeAlarmURL,
	}
	if listener.Force {
		args = append(args, adhdForce)
	}
	return args
}

// adhdListener is where the demo's adhd MCP surface listens and how the cluster
// should reach it. Addr is the adhd --mcp-addr value; Host is a dialable host
// for the MCP URL, the readiness probe, and the discovery registry entry.
type adhdListener struct {
	Addr  string
	Host  string
	Force bool // pass --force: the bind is reachable beyond loopback
}

// mcpURL is the adhd MCP endpoint the cluster advertises for this listener.
func (l adhdListener) mcpURL(port int) string {
	return fmt.Sprintf("http://%s/mcp", net.JoinHostPort(l.Host, strconv.Itoa(port)))
}

// validateADHDBind rejects a bind request that is not an IP address, before any
// listener or child process exists.
func validateADHDBind(bind string) error {
	if bind == "" {
		return nil
	}
	if net.ParseIP(bind) == nil {
		return fmt.Errorf("invalid --adhd-bind %q: give an IP address (for example 127.0.0.1)", bind)
	}
	return nil
}

// resolveADHDListener maps the --adhd-bind host onto the adhd listener and the
// host the cluster advertises.
//
//   - empty: bind all interfaces and advertise the outbound IP — the LAN demo,
//     which needs --force because the endpoint is unguarded (adhd#15);
//   - loopback: bind only loopback and advertise it as-is, with no --force, so
//     the raw adhd is not a LAN bypass around a cluster's gate (lezz.go#58);
//   - an unspecified address: all interfaces again, so it keeps the LAN
//     behavior and the override it requires;
//   - any other IP: bind it and advertise it; it is LAN-reachable, so the
//     unguarded endpoint still needs the override.
func resolveADHDListener(bind string, port int) (adhdListener, error) {
	if err := validateADHDBind(bind); err != nil {
		return adhdListener{}, err
	}
	if bind == "" {
		return adhdListener{Addr: fmt.Sprintf(":%d", port), Host: outboundIP(), Force: true}, nil
	}

	ip := net.ParseIP(bind)
	listener := adhdListener{Addr: net.JoinHostPort(ip.String(), strconv.Itoa(port))}
	if ip.IsUnspecified() {
		listener.Host = outboundIP()
		listener.Force = true
		return listener, nil
	}
	listener.Host = ip.String()
	listener.Force = !ip.IsLoopback()
	return listener, nil
}

// readyTimeout bounds how long a demo waits for a child to open its port.
// A variable, not a constant, so tests can shorten it.
var readyTimeout = 15 * time.Second

// probeADHD asks the installed adhd binary whether it accepts args.
//
// adhd parses its whole command line before honoring -v, and -v exits before
// the daemon starts, so `adhd <args> -v` is a cheap, side-effect-free way to
// ask "would this build accept the command line the demo is about to run?".
// summary is the binary's first line of output: its version on success, its own
// complaint on failure.
func probeADHD(binary string, args []string) (summary string, accepted bool) {
	probeArgs := append(append([]string{}, args...), "-v")
	out, err := exec.Command(binary, probeArgs...).CombinedOutput() //nolint:gosec // binary comes from tools.Find, args are ours
	summary = firstLine(string(out))
	return summary, err == nil
}

// prepareADHD resolves the installed adhd and returns the binary path together
// with the arguments that specific binary accepts. The probe and the start must
// use the same path: resolving "adhd" twice can probe one binary and run
// another (the managed copy in ~/.lezz/bin versus one further along PATH).
func prepareADHD(args []string) (binary string, planned, notes []string, err error) {
	binary, err = tools.Find("adhd")
	if err != nil {
		return "", nil, nil, err
	}
	planned, notes, err = planADHD(binary, args)
	if err != nil {
		return "", nil, nil, err
	}
	return binary, planned, notes, nil
}

// planADHD returns the arguments to start adhd with, checked against what the
// installed binary actually accepts.
//
// A stock `lezz install adhd` resolves to an adhd release that predates
// --force, so adhd exited while parsing its flags and the demo reported only a
// 15 s "adhd MCP readiness" timeout — the actual cause, "flag provided but not
// defined: -force", never reached the operator (#28). The flag is therefore
// probed rather than assumed:
//
//   - the binary accepts the whole command line: run it as built;
//   - it rejects --force and nothing else: run without it, and say so — a build
//     that old also predates the advertise guard, so dropping the flag removes
//     an override of a guard that is not there, rather than removing a guard;
//   - it rejects anything else: fail now, with adhd's own words and the upgrade
//     that fixes it, instead of timing out later.
func planADHD(binary string, args []string) (planned, notes []string, err error) {
	complaint, accepted := probeADHD(binary, args)
	if accepted {
		return args, nil, nil
	}

	stripped, removed := stripFlag(args, adhdForce)
	if removed {
		if version, compatible := probeADHD(binary, stripped); compatible {
			return stripped, []string{fmt.Sprintf(
				"installed %s predates %s; starting without it (that release has no advertise guard to override)",
				version, adhdForce)}, nil
		}
	}

	if complaint == "" {
		complaint = "the binary did not run"
	}
	return nil, nil, fmt.Errorf(
		"adhd rejects the demo's command line: %s\n"+
			"the installed adhd cannot run this cluster; install a release that can: lezz tools upgrade adhd",
		complaint)
}

// stripFlag removes every occurrence of flag from args and reports whether it
// was there. The demo builds these argument lists itself, so a flag's value can
// never be the literal --force.
func stripFlag(args []string, flag string) (rest []string, removed bool) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		if a == flag {
			removed = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, removed
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// watchExit reports the child's exit on a buffered channel so a readiness wait
// can fail the moment the service dies. Without it a crash during startup costs
// the full timeout and hides the reason (#28). Waiting also reaps the child
// instead of leaving a zombie behind.
func watchExit(cmd *exec.Cmd) <-chan error {
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return exited
}

// waitServiceOpen dials addr until it accepts a TCP connection, fails as soon
// as the watched service exits, or readyTimeout passes. A nil channel waits
// only for the port. The reason is bare; readinessError names the service and
// attaches the log once.
func waitServiceOpen(ctx context.Context, addr string, exited <-chan error) error {
	deadline := time.Now().Add(readyTimeout)
	for {
		select {
		case waitErr := <-exited:
			return fmt.Errorf("exited during startup (%w)", waitErr)
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s to open", readyTimeout, addr)
		}
		conn, dialErr := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// readinessError names the service whose readiness failed and attaches the log
// the failure was written to.
func readinessError(service string, err error, logPath string) error {
	return fmt.Errorf("%s readiness: %w; %s", service, err, logEvidence(logPath))
}

// logEvidence keeps the child's log out of the demo's temp root, which Run
// removes on the way out, and renders the last lines plus where the whole log
// now lives. The cause of a failed start therefore outlives the process that
// produced it (#28).
func logEvidence(logPath string) string {
	var b strings.Builder
	if kept := preserveLog(logPath); kept != "" {
		fmt.Fprintf(&b, "full log: %s\n", kept)
	}
	if tail := tailFile(logPath, 20); tail != "" {
		fmt.Fprintf(&b, "--- last 20 lines of %s ---\n%s", logPath, tail)
	}
	if b.Len() == 0 {
		return "the service wrote no output"
	}
	return strings.TrimRight(b.String(), "\n")
}

// preserveLog copies a child's log to ~/.lezz/logs so the evidence survives the
// demo's temp root. It returns the copy's path, or "" when there is nothing to
// keep.
func preserveLog(logPath string) string {
	data, err := os.ReadFile(logPath) //nolint:gosec // logPath is a file in the demo's temp root
	if err != nil || len(data) == 0 {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".lezz", "logs")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return ""
	}
	dest := filepath.Join(dir, "demo-"+filepath.Base(logPath))
	if err := os.WriteFile(dest, data, 0o600); err != nil { //nolint:gosec // dest is ~/.lezz/logs plus filepath.Base(logPath), which cannot traverse
		return ""
	}
	return dest
}
