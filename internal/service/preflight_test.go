package service

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/james-gibson/lezz.go/internal/tools"
)

func TestEndpointArg(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		wantAddr      string
		wantMentioned bool
	}{
		{
			name:          "separate value",
			args:          []string{"--headless", "--mcp-addr", "127.0.0.1:9090"},
			wantAddr:      "127.0.0.1:9090",
			wantMentioned: true,
		},
		{
			name:          "equals form",
			args:          []string{"--headless", "--mcp-addr=127.0.0.1:9091"},
			wantAddr:      "127.0.0.1:9091",
			wantMentioned: true,
		},
		{
			name:          "last occurrence wins",
			args:          []string{"--mcp-addr", "127.0.0.1:9090", "--mcp-addr", "127.0.0.1:9092"},
			wantAddr:      "127.0.0.1:9092",
			wantMentioned: true,
		},
		{
			name:          "no mcp endpoint claimed",
			args:          []string{"--headless"},
			wantMentioned: false,
		},
		{
			name:          "no arguments",
			args:          nil,
			wantMentioned: false,
		},
		{
			name:          "flag without a value",
			args:          []string{"--headless", "--mcp-addr"},
			wantMentioned: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, mentioned := endpointArg(tc.args)
			if addr != tc.wantAddr || mentioned != tc.wantMentioned {
				t.Fatalf("endpointArg(%q) = (%q, %v), want (%q, %v)",
					tc.args, addr, mentioned, tc.wantAddr, tc.wantMentioned)
			}
		})
	}
}

// listenLoopback holds a listener on a free loopback port and returns it with
// the address it owns.
func listenLoopback(t *testing.T) (net.Listener, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, ln.Addr().String()
}

// TestCheckEndpoint_FreeAddressProceeds pins the "proven free" verdict: a
// profile address nobody owns is not a note and not an error.
func TestCheckEndpoint_FreeAddressProceeds(t *testing.T) {
	// Take a port, learn its number, then release it so nothing owns it.
	ln, addr := listenLoopback(t)
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	notes, err := CheckEndpoint("co.james-gibson.lab.adhd.idle",
		[]string{"--headless", "--mcp-addr", addr})
	if err != nil {
		t.Fatalf("free address refused: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("free address produced notes: %v", notes)
	}
}

// TestCheckEndpoint_OwnedAddressRefuses is issue #26's case: another process
// owns the port, adhd would log the bind failure and keep running, and the
// service would be listed as running while serving nothing. The install must
// refuse and name the owner.
func TestCheckEndpoint_OwnedAddressRefuses(t *testing.T) {
	_, addr := listenLoopback(t)

	notes, err := CheckEndpoint("co.james-gibson.lab.adhd.idle",
		[]string{"--headless", "--mcp-addr", addr})
	if !errors.Is(err, ErrMCPPortInUse) {
		t.Fatalf("expected ErrMCPPortInUse, got %v (notes %v)", err, notes)
	}
	_, port, _ := net.SplitHostPort(addr)
	for _, want := range []string{addr, port, "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err, want)
		}
	}
}

// TestCheckEndpoint_UnprovableWarns pins the other half of the pattern: a probe
// that proves nothing must not block the install.
func TestCheckEndpoint_UnprovableWarns(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{
			// TEST-NET-1 (RFC 5737): never an address of this host, so the bind
			// fails with "can't assign requested address", not EADDRINUSE.
			name: "address not local to this host",
			addr: "192.0.2.1:9090",
		},
		{
			name: "not a host:port pair",
			addr: "127.0.0.1",
		},
		{
			name: "port is not a number",
			addr: "127.0.0.1:http",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notes, err := CheckEndpoint("co.james-gibson.lab.adhd.idle",
				[]string{"--headless", "--mcp-addr", tc.addr})
			if err != nil {
				t.Fatalf("unprovable address refused: %v", err)
			}
			if len(notes) != 1 {
				t.Fatalf("expected one warning, got %v", notes)
			}
			if !strings.Contains(notes[0], tc.addr) {
				t.Fatalf("warning %q does not name the address %q", notes[0], tc.addr)
			}
		})
	}
}

// TestCheckEndpoint_NoClaimDoesNothing: a profile that names no MCP endpoint
// (ocd-smoke-alarm idle, tuner idle, lezz demo) must not be probed.
func TestCheckEndpoint_NoClaimDoesNothing(t *testing.T) {
	notes, err := CheckEndpoint("co.james-gibson.lab.tuner.idle", nil)
	if err != nil || len(notes) != 0 {
		t.Fatalf("unclaimed endpoint: notes %v, err %v", notes, err)
	}
}

// TestCheckEndpoint_PortZeroSkipped: --mcp-addr :0 means "any free port", so
// no conflict is possible and the install must not warn about it.
func TestCheckEndpoint_PortZeroSkipped(t *testing.T) {
	notes, err := CheckEndpoint("co.james-gibson.lab.adhd.idle",
		[]string{"--headless", "--mcp-addr", "127.0.0.1:0"})
	if err != nil || len(notes) != 0 {
		t.Fatalf("ephemeral port: notes %v, err %v", notes, err)
	}
}

func TestHeldByOwnInstance(t *testing.T) {
	cases := []struct {
		name   string
		owners []portOwner
		own    int
		want   bool
	}{
		{name: "our own running job", owners: []portOwner{{pid: 4711, command: "adhd"}}, own: 4711, want: true},
		{name: "someone else", owners: []portOwner{{pid: 1202, command: "adhd"}}, own: 4711},
		{name: "another process alongside ours", owners: []portOwner{{pid: 4711}, {pid: 1202}}, own: 4711},
		{name: "no owner identified", owners: nil, own: 4711},
		{name: "label not running", owners: []portOwner{{pid: 1202}}, own: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := heldByOwnInstanceWith(tc.owners, tc.own); got != tc.want {
				t.Fatalf("heldByOwnInstanceWith(%v, %d) = %v, want %v", tc.owners, tc.own, got, tc.want)
			}
		})
	}
}

func TestDescribeOwners(t *testing.T) {
	cases := []struct {
		name   string
		owners []portOwner
		want   string
	}{
		{name: "none", want: "a process lsof could not identify"},
		{name: "one", owners: []portOwner{{pid: 1202, command: "adhd"}}, want: "PID 1202 (adhd)"},
		{
			name:   "two",
			owners: []portOwner{{pid: 1202, command: "adhd"}, {pid: 1203, command: "adhd"}},
			want:   "PID 1202 (adhd), PID 1203 (adhd)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeOwners(tc.owners); got != tc.want {
				t.Fatalf("describeOwners(%v) = %q, want %q", tc.owners, got, tc.want)
			}
		})
	}
}

// TestInstall_RefusesBeforeWritingPlist is the guard at the artifact: on a
// proven conflict Install returns ErrMCPPortInUse and leaves no plist behind,
// so the service cannot appear in `lezz service list` at all.
func TestInstall_RefusesBeforeWritingPlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd install is macOS-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, addr := listenLoopback(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	tool := tools.Tool{
		Name: "adhd",
		Profiles: []tools.DaemonProfile{{
			Name: "idle",
			Args: []string{"--headless", "--mcp-addr", addr},
		}},
	}

	_, err = Install(tool, tool.Profiles[0], "/bin/echo", false)
	if !errors.Is(err, ErrMCPPortInUse) {
		t.Fatalf("expected ErrMCPPortInUse, got %v", err)
	}
	if !strings.Contains(err.Error(), port) {
		t.Fatalf("refusal %q does not name port %s", err, port)
	}

	plist := filepath.Join(home, "Library", "LaunchAgents",
		"co.james-gibson.lab.adhd.idle.plist")
	if _, statErr := os.Stat(plist); statErr == nil {
		t.Fatalf("plist written despite the refusal: %s", plist)
	}
	if _, statErr := os.Stat(filepath.Join(home, "Library", "LaunchAgents")); statErr == nil {
		t.Fatalf("LaunchAgents dir created despite the refusal")
	}
}

// TestInstall_RefusesOnNonDarwin keeps the platform error ahead of the probe on
// a host with no launchd — a port conflict there is not the reason to refuse.
func TestInstall_RefusesOnNonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("covered on macOS by the plist test")
	}
	_, err := Install(tools.Tool{Name: "adhd"}, tools.DaemonProfile{Name: "idle"}, "/bin/echo", false)
	if err == nil || errors.Is(err, ErrMCPPortInUse) {
		t.Fatalf("expected the platform error, got %v", err)
	}
}

// TestEndpointArgDoesNotEatFlags guards the flag-semantics assumption: a
// value that looks like another flag is still the value, which is what the
// flag package would do too.
func TestEndpointArgDoesNotEatFlags(t *testing.T) {
	addr, mentioned := endpointArg([]string{"--mcp-addr", "--force"})
	if !mentioned || addr != "--force" {
		t.Fatalf("endpointArg = (%q, %v), want (--force, true)", addr, mentioned)
	}
}

// TestServicePIDUnloaded: an unloaded label reports no pid, so a reinstall of a
// service that is not running probes normally.
func TestServicePIDUnloaded(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchctl is macOS-only")
	}
	if pid := servicePID("co.james-gibson.lab.does-not-exist.test"); pid != 0 {
		t.Fatalf("unloaded label reported pid %d", pid)
	}
}
