package demo

import (
	"strings"
	"testing"
)

func TestResolveADHDListener(t *testing.T) {
	const port = 52102

	cases := []struct {
		name      string
		bind      string
		wantAddr  string
		wantHost  string
		wantForce bool
	}{
		{
			name:      "default binds all interfaces and advertises the LAN address",
			bind:      "",
			wantAddr:  ":52102",
			wantHost:  outboundIP(),
			wantForce: true,
		},
		{
			name:      "loopback binds loopback and needs no override",
			bind:      "127.0.0.1",
			wantAddr:  "127.0.0.1:52102",
			wantHost:  "127.0.0.1",
			wantForce: false,
		},
		{
			name:      "IPv6 loopback keeps its brackets",
			bind:      "::1",
			wantAddr:  "[::1]:52102",
			wantHost:  "::1",
			wantForce: false,
		},
		{
			name:      "an unspecified address is the LAN default",
			bind:      "0.0.0.0",
			wantAddr:  "0.0.0.0:52102",
			wantHost:  outboundIP(),
			wantForce: true,
		},
		{
			name:      "a specific LAN address is advertised as itself",
			bind:      "192.0.2.10",
			wantAddr:  "192.0.2.10:52102",
			wantHost:  "192.0.2.10",
			wantForce: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := resolveADHDListener(tc.bind, port)
			if err != nil {
				t.Fatalf("resolveADHDListener(%q): %v", tc.bind, err)
			}
			if listener.Addr != tc.wantAddr {
				t.Errorf("Addr = %q, want %q", listener.Addr, tc.wantAddr)
			}
			if listener.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", listener.Host, tc.wantHost)
			}
			if listener.Force != tc.wantForce {
				t.Errorf("Force = %v, want %v", listener.Force, tc.wantForce)
			}
		})
	}
}

func TestResolveADHDListenerRejectsAHostname(t *testing.T) {
	_, err := resolveADHDListener("localhost", 52102)
	if err == nil {
		t.Fatal("expected a hostname to be rejected")
	}
	for _, want := range []string{"--adhd-bind", "localhost", "127.0.0.1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestValidateADHDBindAllowsEmpty(t *testing.T) {
	if err := validateADHDBind(""); err != nil {
		t.Fatalf("empty bind rejected: %v", err)
	}
}

func TestADHDMCPSURL(t *testing.T) {
	v4, err := resolveADHDListener("127.0.0.1", 52102)
	if err != nil {
		t.Fatalf("resolve v4: %v", err)
	}
	if got, want := v4.mcpURL(52102), "http://127.0.0.1:52102/mcp"; got != want {
		t.Errorf("mcpURL = %q, want %q", got, want)
	}

	v6, err := resolveADHDListener("::1", 52102)
	if err != nil {
		t.Fatalf("resolve v6: %v", err)
	}
	if got, want := v6.mcpURL(52102), "http://[::1]:52102/mcp"; got != want {
		t.Errorf("mcpURL = %q, want %q", got, want)
	}
}

// TestDemoADHDArgsOmitsForceOnLoopback is the contract in lezz.go#58: the
// advertise-guard override exists for a LAN bind, so a loopback bound adhd is
// started without it — and the binary is told to bind loopback, not all
// interfaces.
func TestDemoADHDArgsOmitsForceOnLoopback(t *testing.T) {
	loopback, err := resolveADHDListener("127.0.0.1", 52102)
	if err != nil {
		t.Fatalf("resolve loopback: %v", err)
	}
	args := demoADHDArgs("/cfg", "/log", loopback, "http://0.0.0.0:19001")

	if got := argAfter(args, "--mcp-addr"); got != "127.0.0.1:52102" {
		t.Errorf("--mcp-addr = %q, want 127.0.0.1:52102", got)
	}
	for _, a := range args {
		if a == adhdForce {
			t.Fatalf("loopback command line still passes %s: %q", adhdForce, args)
		}
	}
}

func TestDemoADHDArgsKeepsForceForTheLANDefault(t *testing.T) {
	lan, err := resolveADHDListener("", 52102)
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	args := demoADHDArgs("/cfg", "/log", lan, "http://0.0.0.0:19001")

	if got := argAfter(args, "--mcp-addr"); got != ":52102" {
		t.Errorf("--mcp-addr = %q, want :52102", got)
	}
	if !hasArg(args, adhdForce) {
		t.Fatalf("LAN command line dropped %s: %q", adhdForce, args)
	}
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
