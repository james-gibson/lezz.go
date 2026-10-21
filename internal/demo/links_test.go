package demo

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBuildLinkConfig checks the in-process alarm config against the two
// failure modes the old generated YAML had: a subsystem the facade cannot run
// (remote agent managed updates), and the lock file it must not write.
func TestBuildLinkConfig(t *testing.T) {
	stateDir := t.TempDir()
	cfg, err := buildLinkConfig(linkParams{
		Port:       19001,
		ListenAddr: "127.0.0.1",
		StateDir:   stateDir,
		PeerName:   "alarm-b",
		PeerPort:   19002,
		Advertise:  false,
	})
	if err != nil {
		t.Fatalf("buildLinkConfig: %v", err)
	}

	if !cfg.Health.Enabled || !cfg.Health.SelfCheck {
		t.Errorf("health = enabled:%v self_check:%v, want both true", cfg.Health.Enabled, cfg.Health.SelfCheck)
	}
	if want := "127.0.0.1:19001"; cfg.Health.ListenAddr != want {
		t.Errorf("health.listen_addr = %q, want %q", cfg.Health.ListenAddr, want)
	}
	if cfg.Runtime.StateDir != stateDir {
		t.Errorf("runtime.state_dir = %q, want %q", cfg.Runtime.StateDir, stateDir)
	}
	if want := stateDir + "/known-good.json"; cfg.Runtime.BaselineFile != want {
		t.Errorf("runtime.baseline_file = %q, want %q", cfg.Runtime.BaselineFile, want)
	}
	if !cfg.KnownState.Enabled {
		t.Error("known_state.enabled = false, want true; the link would establish no baseline")
	}
	if !cfg.KnownState.Persist {
		t.Error("known_state.persist = false, want true")
	}
	if cfg.Tuner.Advertise {
		t.Error("tuner.advertise = true, want false for the params given")
	}
	if want := "_smoke-alarm._tcp"; cfg.Tuner.ServiceType != want {
		t.Errorf("tuner.service_type = %q, want %q; without it the advertiser cannot register", cfg.Tuner.ServiceType, want)
	}
	if !cfg.Alerts.Sinks.Log.Enabled {
		t.Error("alerts.sinks.log.enabled = false, want true")
	}
	if cfg.RemoteAgent.ManagedUpdates {
		t.Error("remote_agent.managed_updates = true; it describes managing a process and has no meaning in-process")
	}

	if len(cfg.Targets) != 1 {
		t.Fatalf("targets = %d, want 1 hand-peered target", len(cfg.Targets))
	}
	peer := cfg.Targets[0]
	if peer.ID != "peer" || peer.Name != "alarm-b" {
		t.Errorf("peer target = id %q name %q, want %q / %q", peer.ID, peer.Name, "peer", "alarm-b")
	}
	if want := "http://localhost:19002/status"; peer.Endpoint != want {
		t.Errorf("peer endpoint = %q, want %q", peer.Endpoint, want)
	}
}

// TestInProcessLinkPair starts two links in this process and checks that each
// serves its health endpoints, registers the self-check probe, and reports the
// other as a target. It also checks that Shutdown releases the listeners.
func TestInProcessLinkPair(t *testing.T) {
	if testing.Short() {
		t.Skip("starts two in-process alarms")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	a, b, baselineA, baselineB := startTestPair(t, ctx)

	// Readiness: Start returns once the listener is bound and serving.
	waitForStatus(t, a.healthURL())

	statusA := waitForStatus(t, statusURL(t, a), "peer", "self-health-check")
	statusB := waitForStatus(t, statusURL(t, b), "peer", "self-health-check")
	if !strings.Contains(statusA, fmt.Sprintf("localhost:%d", mustPort(t, b))) {
		t.Errorf("alarm-a /status does not name alarm-b's actual port %d:\n%s", mustPort(t, b), statusA)
	}
	if !strings.Contains(statusB, fmt.Sprintf("localhost:%d", mustPort(t, a))) {
		t.Errorf("alarm-b /status does not name alarm-a's actual port %d:\n%s", mustPort(t, a), statusB)
	}

	// Known-state baselines are per link: a successful check persists each
	// link's own baseline file under its own state dir.
	waitForFile(t, baselineA, baselineB)

	healthA, healthB := a.healthURL(), b.healthURL()
	a.shutdown()
	b.shutdown()
	for _, url := range []string{healthA, healthB} {
		if err := dialURL(url); err == nil {
			t.Errorf("%s still accepts connections after Shutdown", url)
		}
	}
}

// startTestPair allocates two distinct ports, starts both links on loopback
// with mDNS advertising off, and fails the test if the pair cannot be peered.
func startTestPair(t *testing.T, ctx context.Context) (*inProcessLink, *inProcessLink, string, string) {
	t.Helper()

	for attempt := 0; attempt < 3; attempt++ {
		portA := mustFreePort(t)
		portB := mustFreePort(t)
		if portB == portA {
			continue
		}

		logA := t.TempDir() + "/alarm-a.log"
		logB := t.TempDir() + "/alarm-b.log"
		stateA := tempStateDir(t)
		stateB := tempStateDir(t)
		paramsA := linkParams{
			Port:       portA,
			ListenAddr: "127.0.0.1",
			StateDir:   stateA,
			PeerName:   "alarm-b",
			PeerPort:   portB,
			Advertise:  false,
		}
		paramsB := linkParams{
			Port:       portB,
			ListenAddr: "127.0.0.1",
			StateDir:   stateB,
			PeerName:   "alarm-a",
			PeerPort:   portA,
			Advertise:  false,
		}

		a, err := startLink(ctx, "alarm-a", paramsA, logA)
		if err != nil {
			t.Fatalf("start alarm-a: %v", err)
		}
		b, err := startLink(ctx, "alarm-b", paramsB, logB)
		if err != nil {
			a.shutdown()
			t.Fatalf("start alarm-b: %v", err)
		}
		if err := checkLinkPorts(a, portA, b, portB); err != nil {
			// A port was taken between freePort and bind; retry with fresh ones.
			a.shutdown()
			b.shutdown()
			continue
		}

		t.Cleanup(func() {
			a.shutdown()
			b.shutdown()
		})
		return a, b, stateA + "/known-good.json", stateB + "/known-good.json"
	}

	t.Fatal("could not bind two distinct loopback ports for the link pair")
	return nil, nil, "", ""
}

func statusURL(t *testing.T, l *inProcessLink) string {
	t.Helper()
	port, err := l.port()
	if err != nil {
		t.Fatalf("link port: %v", err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d/status", port)
}

func mustPort(t *testing.T, l *inProcessLink) int {
	t.Helper()
	port, err := l.port()
	if err != nil {
		t.Fatalf("link port: %v", err)
	}
	return port
}

// tempStateDir is t.TempDir for directories an embedded engine writes to. The
// write on shutdown is asynchronous (the facade cancels the engine's context and
// returns), so a file may land while TempDir's own cleanup is walking the tree;
// ignoring the removal error keeps that race out of the test result.
func tempStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lezz-link-state-*")
	if err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func mustFreePort(t *testing.T) int {
	t.Helper()
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	return port
}

// waitForStatus polls url until the body contains every want string.
func waitForStatus(t *testing.T, url string, want ...string) string {
	t.Helper()
	return pollBody(t, url, 15*time.Second, want...)
}

func pollBody(t *testing.T, url string, timeout time.Duration, want ...string) string {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var last string
	for {
		resp, err := client.Get(url) //nolint:gosec // url is a loopback health endpoint built in this test
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil {
				last = string(body)
				if resp.StatusCode == http.StatusOK && containsAll(last, want) {
					return last
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s to contain %q; last body:\n%s", url, want, last)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForFile waits until every path exists, proving each link persisted its
// own known-state baseline.
func waitForFile(t *testing.T, paths ...string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		missing := ""
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				missing = path
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for known-state baseline %s", missing)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func containsAll(body string, want []string) bool {
	for _, w := range want {
		if !strings.Contains(body, w) {
			return false
		}
	}
	return true
}

func dialURL(url string) error {
	addr := strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
	addr = strings.TrimSuffix(addr, "/healthz")
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}
