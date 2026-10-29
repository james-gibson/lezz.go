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
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDemoRegistryStaysOnLoopback is the acceptance test for lezz.go#59: a demo
// started with no flags serves /cluster on loopback, refuses connections on the
// LAN address, and says so in the state file — so `lezz demo status` probes the
// endpoint that actually answers.
//
// Both verbs matter, because the registry is a read-and-write directory: a LAN
// caller could enumerate every cluster's endpoints (including a raw adhd URL)
// and POST a record of its own over an existing name.
//
// The cluster is real — both alarms run in-process — but adhd is a stand-in
// built by the test, because CI has no installed adhd.
func TestDemoRegistryStaysOnLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a full demo cluster")
	}
	startRegistryDemo(t, Options{})

	lanIP := outboundIP()
	loopbackURL := fmt.Sprintf("http://127.0.0.1:%d", DiscoveryPort)

	// The directory answers the host that owns it.
	if got := getClusters(t, loopbackURL); len(got) == 0 {
		t.Fatalf("registry on loopback is empty: %v", got)
	}

	if ip := net.ParseIP(lanIP); ip == nil || ip.IsLoopback() {
		t.Logf("no LAN address on this host (outbound IP %q); skipping the off-LAN assertions", lanIP)
		return
	}

	// GET (enumeration) and POST (poisoning) are both refused, because the
	// listener is not there at all.
	lanBase := fmt.Sprintf("http://%s:%d", lanIP, DiscoveryPort)
	if got := getClustersMaybe(t, lanBase); got != nil {
		t.Fatalf("GET %s/cluster answered from the LAN: %v", lanBase, got)
	}
	body := []byte(`{"name":"poisoned","alarm_a":"http://evil"}`)
	if code, err := postMaybe(t, lanBase+"/cluster", body); err == nil {
		t.Fatalf("POST %s/cluster from the LAN was accepted (HTTP %d)", lanBase, code)
	}

	// And the host that owns it still sees only what it registered.
	if _, ok := getClusters(t, loopbackURL)["poisoned"]; ok {
		t.Fatal("a LAN POST registered a record in the local registry")
	}
}

// TestDemoRegistryPublishesOnlyWhenAsked covers the deliberate LAN case: the
// bind is an operator decision, and once taken the directory is reachable —
// anonymously, by design, so an unknown alarm can register.
func TestDemoRegistryPublishesOnlyWhenAsked(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a full demo cluster")
	}
	startRegistryDemo(t, Options{RegistryBind: "0.0.0.0"})

	lanIP := outboundIP()
	if ip := net.ParseIP(lanIP); ip == nil || ip.IsLoopback() {
		t.Skipf("no LAN address on this host (outbound IP %q)", lanIP)
	}

	lanBase := fmt.Sprintf("http://%s:%d", lanIP, DiscoveryPort)
	clusters := getClusters(t, lanBase)
	if len(clusters) == 0 {
		t.Fatalf("a published registry is empty: %v", clusters)
	}
}

// TestDemoReportsTheRegistryBindItUses asserts the state file names the bind,
// not an outbound IP, so `lezz demo status` does not probe an address nothing
// answers on.
func TestDemoReportsTheRegistryBindItUses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a full demo cluster")
	}
	state := startRegistryDemo(t, Options{})
	want := fmt.Sprintf("http://127.0.0.1:%d/cluster", DiscoveryPort)
	if state.Discovery != want {
		t.Fatalf("state discovery = %q, want %q", state.Discovery, want)
	}
}

// startRegistryDemo runs a demo cluster under a temp HOME and returns the state
// it published. The cluster is torn down with the test.
func startRegistryDemo(t *testing.T, opts Options) State {
	t.Helper()
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
	stateDir, err := ResolveStateDir(opts.StateDir)
	if err != nil {
		t.Fatalf("resolve state dir: %v", err)
	}
	binDir := filepath.Join(home, ".lezz", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	buildStandInADHD(t, filepath.Join(binDir, "adhd"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = Run(ctx, opts)
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

	return waitForDemoState(t, done, func() error { return runErr }, stateDir)
}

// getClustersMaybe is getClusters without the failure: an unreachable or
// non-200 registry returns nil, which is the assertion a LAN caller needs.
func getClustersMaybe(t *testing.T, baseURL string) map[string]ClusterInfo {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/cluster", http.NoBody)
	if err != nil {
		t.Fatalf("new GET request: %v", err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var got map[string]ClusterInfo
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		return nil
	}
	return got
}

// postMaybe POSTs and returns the status code, or the dial error.
func postMaybe(t *testing.T, url string, body []byte) (int, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new POST request: %v", err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// TestDemoRegistryBindIsAPreflight fails the start before anything is spawned:
// a bad bind must not survive into a detached child's log.
func TestDemoRegistryBindIsAPreflight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := Run(context.Background(), Options{RegistryBind: "localhost"})
	if err == nil {
		t.Fatal("expected a hostname registry bind to be rejected")
	}
	if !strings.Contains(err.Error(), "--registry-bind") {
		t.Fatalf("error %q does not name the flag", err)
	}

	// The detached path checks the parent's own argv for the same reason.
	if _, err := StartDetached(context.Background(), Options{RegistryBind: "localhost"}); err == nil {
		t.Fatal("demo start accepted a hostname registry bind")
	}
}
