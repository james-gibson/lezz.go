package demo

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDemoLoopbackBindKeepsADHDOffTheLAN is the acceptance test for lezz.go#58:
// a demo started with --adhd-bind 127.0.0.1 serves /mcp on loopback, does not
// serve it on the LAN address, and publishes the loopback URL in the registry,
// so the cluster's self-join and notify paths agree with what it advertises.
//
// The cluster is real — both alarms run in-process — but adhd is a stand-in
// built by this test, because CI has no installed adhd.
func TestDemoLoopbackBindKeepsADHDOffTheLAN(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a full demo cluster")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("building the stand-in adhd needs the go command: %v", err)
	}
	// The registry port is fixed, so this test owns the whole host's demo
	// namespace: skip rather than join a real cluster that is already running.
	if !listeningAllInterfacesFree(DiscoveryPort) {
		t.Skipf("discovery port %d is already in use on this host", DiscoveryPort)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	stateDir, err := ResolveStateDir("")
	if err != nil {
		t.Fatalf("resolve state dir: %v", err)
	}
	binDir := filepath.Join(home, ".lezz", "bin")
	if mkErr := os.MkdirAll(binDir, 0o750); mkErr != nil {
		t.Fatalf("create bin dir: %v", mkErr)
	}
	buildStandInADHD(t, filepath.Join(binDir, "adhd"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = Run(ctx, Options{ADHDBind: "127.0.0.1"})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			if runErr != nil {
				t.Errorf("Run returned %v, want a clean shutdown", runErr)
			}
		case <-time.After(30 * time.Second):
			t.Error("demo did not stop after its context was canceled")
		}
	})

	state := waitForDemoState(t, done, func() error { return runErr }, stateDir)
	if state.Ports.ADHD == 0 {
		t.Fatalf("state has no adhd port: %+v", state.Ports)
	}
	wantURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", state.Ports.ADHD)
	if state.Cluster.AdhdMCP != wantURL {
		t.Fatalf("state adhd_mcp = %q, want %q", state.Cluster.AdhdMCP, wantURL)
	}

	// The MCP surface answers on loopback. The status is not the point — the
	// real adhd answers 500 to a bare GET of its SSE endpoint — reaching it is.
	waitForHTTP(t, wantURL)

	// The registry carries the same loopback URL, rather than an outbound IP
	// the caller cannot reach through a fronting gate.
	clusters := getClusters(t, fmt.Sprintf("http://127.0.0.1:%d", DiscoveryPort))
	entry, ok := clusters[state.Cluster.Name]
	if !ok {
		t.Fatalf("registry has no entry for %q: %v", state.Cluster.Name, clusters)
	}
	if entry.AdhdMCP != wantURL {
		t.Fatalf("registry adhd_mcp = %q, want %q", entry.AdhdMCP, wantURL)
	}

	// And it does not answer on the LAN address. A host with no LAN interface
	// cannot make this assertion.
	lanIP := outboundIP()
	if ip := net.ParseIP(lanIP); ip == nil || ip.IsLoopback() {
		t.Logf("no LAN address on this host (outbound IP %q); skipping the off-LAN assertion", lanIP)
		return
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(lanIP, strconv.Itoa(state.Ports.ADHD)), 2*time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("adhd MCP answers on the LAN address %s:%d; --adhd-bind 127.0.0.1 must keep it on loopback", lanIP, state.Ports.ADHD)
	}
}

// waitForDemoState waits for the cluster to publish its endpoints in stateDir,
// failing if it exits first so a startup error is reported rather than a 60 s
// timeout.
func waitForDemoState(t *testing.T, done <-chan struct{}, runErr func() error, stateDir string) State {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case <-done:
			t.Fatalf("demo exited before publishing state: %v", runErr())
		default:
		}
		if state, ok := readState(stateDir); ok {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatal("demo did not publish ~/.lezz/demo-state.json")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
		if err != nil {
			t.Fatalf("new GET request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", url, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// listeningAllInterfacesFree reports whether the demo's registry could bind the
// fixed port the way startDiscoveryServer does. portFree only probes loopback,
// which is not enough here: another process can hold the IPv6 wildcard while
// 127.0.0.1 is still bindable.
func listeningAllInterfacesFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// buildStandInADHD builds an adhd that accepts the demo's command line, answers
// -v the way the real probe expects, and serves /mcp on whatever --mcp-addr it
// is given. Binding what it is told is the behavior under test.
func buildStandInADHD(t *testing.T, dest string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(src, []byte(standInADHDSource), 0o600); err != nil {
		t.Fatalf("write stand-in adhd: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", dest, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stand-in adhd: %v\n%s", err, out)
	}
}

const standInADHDSource = `package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
)

func main() {
	var accepted struct {
		headless   bool
		demo       bool
		force      bool
		config     string
		logPath    string
		smokeAlarm string
		mcpAddr    string
	}
	flag.BoolVar(&accepted.headless, "headless", false, "")
	flag.BoolVar(&accepted.demo, "demo", false, "")
	flag.BoolVar(&accepted.force, "force", false, "")
	flag.StringVar(&accepted.config, "config", "", "")
	flag.StringVar(&accepted.logPath, "log", "", "")
	flag.StringVar(&accepted.smokeAlarm, "smoke-alarm", "", "")
	flag.StringVar(&accepted.mcpAddr, "mcp-addr", "", "")
	showVersion := flag.Bool("v", false, "")
	flag.Parse()

	if *showVersion {
		fmt.Println("adhd v0.9.0-stand-in")
		return
	}

	ln, err := net.Listen("tcp", accepted.mcpAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stand-in adhd: listen %s: %v\n", accepted.mcpAddr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "stand-in adhd")
	})
	_ = http.Serve(ln, mux)
}
`

// TestDemoRejectsAHostnameBind fails the start before anything is spawned: the
// bind is an address, not a name to resolve.
func TestDemoRejectsAHostnameBind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := Run(context.Background(), Options{ADHDBind: "localhost"})
	if err == nil {
		t.Fatal("expected a hostname bind to be rejected")
	}
	if !strings.Contains(err.Error(), "--adhd-bind") {
		t.Fatalf("error %q does not name the flag", err)
	}
}
