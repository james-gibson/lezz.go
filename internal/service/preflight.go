package service

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// ErrMCPPortInUse is returned by CheckEndpoint when the MCP address a daemon
// profile claims is proven to belong to another process.
//
// adhd logs a failed bind as a warning and keeps running
// (cmd/adhd/main.go: slog.Warn "headless server start warning"), so a service
// installed over a taken port comes up green in `lezz service list` while
// exposing no MCP server — the same observable outcome as #11 (a daemon that
// serves nothing), reached by a different route. The claim in the profile's
// --mcp-addr is therefore probed before the plist is written, and a proven
// conflict is fatal so the offense cannot ship.
var ErrMCPPortInUse = errors.New("refusing to install a service whose MCP port is in use")

// mcpAddrFlag is adhd's flag for the MCP server: passing it both enables the
// server and sets the address it binds.
const mcpAddrFlag = "--mcp-addr"

// CheckEndpoint is the fail-closed preflight for `lezz service install`. It
// derives the address the profile claims from its --mcp-addr argument and
// probes it: net.Listen on host:port, closed immediately, proves whether
// anything else already owns it.
//
// The verdict follows the unshippable-offense pattern — refuse on proven
// conflict, warn on anything unprovable:
//
//   - another process owns the port: ErrMCPPortInUse, and nothing is written;
//   - the port is held by the currently installed instance of this very label:
//     expected, because Install unloads it before loading the replacement;
//   - the address cannot be probed (unparseable, not fixed, not local to this
//     host, no permission): a note, and the install goes ahead, because
//     "could not prove" is not evidence of a conflict;
//   - the profile claims no MCP address at all: nothing to check.
//
// The returned notes are operator-facing: the caller prints them, the library
// owns no output.
func CheckEndpoint(label string, args []string) (notes []string, err error) {
	addr, mentioned := endpointArg(args)
	if !mentioned {
		return nil, nil
	}
	if addr == "" {
		return []string{fmt.Sprintf("profile passes %s with no value; nothing to probe", mcpAddrFlag)}, nil
	}

	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return []string{fmt.Sprintf("cannot probe MCP address %q: %v", addr, splitErr)}, nil
	}
	n, numeric := parsePort(port)
	switch {
	case numeric && n == 0:
		// Port 0 asks the kernel for any free port, so nothing can already be
		// holding it and there is nothing to prove.
		return nil, nil
	case !numeric || n < 0 || n > 65535:
		return []string{fmt.Sprintf("cannot probe MCP address %q: %q is not a fixed port", addr, port)}, nil
	}

	// A wildcard bind answers on loopback too, so probe a concrete address.
	probe := net.JoinHostPort(probeHost(host), port)
	ln, listenErr := net.Listen("tcp", probe)
	if listenErr == nil {
		_ = ln.Close()
		return nil, nil
	}
	if !isAddrInUse(listenErr) {
		return []string{fmt.Sprintf("could not prove MCP address %s is free: %v", probe, listenErr)}, nil
	}

	owners := portOwners(port)
	if heldByOwnInstance(owners, label) {
		return []string{fmt.Sprintf("MCP address %s is held by the running %s; lezz reloads it", probe, label)}, nil
	}

	return nil, fmt.Errorf("%w: %s is held by %s; stop it or choose another %s, or pass --force to install anyway",
		ErrMCPPortInUse, probe, describeOwners(owners), mcpAddrFlag)
}

// endpointArg returns the MCP address named by args, applying flag semantics:
// the last occurrence wins, and both `--mcp-addr ADDR` and
// `--mcp-addr=ADDR` count. mentioned reports whether the flag appears at all,
// which distinguishes "no MCP endpoint" from "an MCP endpoint with no value".
func endpointArg(args []string) (addr string, mentioned bool) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == mcpAddrFlag:
			mentioned = true
			if i+1 < len(args) {
				addr = args[i+1]
				i++
			}
		case strings.HasPrefix(args[i], mcpAddrFlag+"="):
			mentioned = true
			addr = strings.TrimPrefix(args[i], mcpAddrFlag+"=")
		}
	}
	return addr, mentioned
}

// parsePort reads a decimal port number. numeric is false when the value is not
// a number at all, which is a different verdict from a number out of range.
func parsePort(port string) (n int, numeric bool) {
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0, false
	}
	return n, true
}

// probeHost maps a wildcard bind host onto a concrete loopback address to
// probe: a listener on the unspecified address answers on loopback too.
func probeHost(host string) string {
	if host == "" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "127.0.0.1"
	}
	return host
}

// isAddrInUse reports whether err is the kernel refusing a bind because
// someone else owns the address. The message test keeps that verdict on
// platforms whose errno does not map onto syscall.EADDRINUSE.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) ||
		strings.Contains(strings.ToLower(err.Error()), "address already in use")
}

// portOwner is one process listening on a port, as identified by lsof.
type portOwner struct {
	pid     int
	command string
}

// portOwners asks lsof who is listening on port. An empty result is not
// evidence that the port is free: it means the owner could not be identified.
func portOwners(port string) []portOwner {
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fpc").Output() //nolint:gosec // port is a validated decimal number
	if err != nil && len(out) == 0 {
		return nil
	}

	var (
		owners  []portOwner
		current portOwner
	)
	flush := func() {
		if current.pid != 0 {
			owners = append(owners, current)
		}
		current = portOwner{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			flush()
			if pid, convErr := strconv.Atoi(line[1:]); convErr == nil {
				current.pid = pid
			}
		case 'c':
			current.command = line[1:]
		}
	}
	flush()
	return owners
}

// heldByOwnInstance reports whether every process listening on the port is the
// launchd job we are about to replace. That is the ordinary reinstall path:
// the running instance owns the port until Install unloads it.
func heldByOwnInstance(owners []portOwner, label string) bool {
	return heldByOwnInstanceWith(owners, servicePID(label))
}

// heldByOwnInstanceWith is heldByOwnInstance with the label's pid supplied, so
// the ownership rule can be tested without a loaded launchd job.
func heldByOwnInstanceWith(owners []portOwner, ownPID int) bool {
	if ownPID == 0 || len(owners) == 0 {
		return false
	}
	for _, o := range owners {
		if o.pid != ownPID {
			return false
		}
	}
	return true
}

// servicePID returns the pid launchd reports for label, or 0 when the label is
// not loaded or not running.
func servicePID(label string) int {
	out, err := exec.Command("launchctl", "list", label).Output() //nolint:gosec // label is built from the registry, never from user input
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		rest, found := strings.CutPrefix(strings.TrimSpace(line), `"PID" = `)
		if !found {
			continue
		}
		rest = strings.TrimSuffix(rest, ";")
		if pid, convErr := strconv.Atoi(rest); convErr == nil {
			return pid
		}
	}
	return 0
}

// describeOwners names the processes holding a port, for the refusal message.
func describeOwners(owners []portOwner) string {
	switch len(owners) {
	case 0:
		return "a process lsof could not identify"
	case 1:
		return fmt.Sprintf("PID %d (%s)", owners[0].pid, owners[0].command)
	}
	parts := make([]string, 0, len(owners))
	for _, o := range owners {
		parts = append(parts, fmt.Sprintf("PID %d (%s)", o.pid, o.command))
	}
	return strings.Join(parts, ", ")
}
