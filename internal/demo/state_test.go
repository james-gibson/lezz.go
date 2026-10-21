package demo

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolateHome points ~/.lezz at a temp dir for the duration of the test.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	return home
}

func TestResolvePorts_ReusesSavedPorts(t *testing.T) {
	isolateHome(t)

	first, err := resolvePorts(Options{})
	if err != nil {
		t.Fatalf("resolvePorts: %v", err)
	}
	if first.AlarmA == 0 || first.AlarmB == 0 || first.ADHD == 0 {
		t.Fatalf("resolvePorts returned an empty port: %+v", first)
	}
	if first.AlarmA == first.AlarmB || first.AlarmA == first.ADHD || first.AlarmB == first.ADHD {
		t.Fatalf("resolvePorts returned duplicate ports: %+v", first)
	}
	if saveErr := savePorts(first); saveErr != nil {
		t.Fatalf("savePorts: %v", saveErr)
	}

	second, err := resolvePorts(Options{})
	if err != nil {
		t.Fatalf("resolvePorts (second): %v", err)
	}
	if second.AlarmA != first.AlarmA || second.AlarmB != first.AlarmB || second.ADHD != first.ADHD {
		t.Errorf("ports moved between runs: first %+v, second %+v", first, second)
	}
}

func TestResolvePorts_PinnedPortWins(t *testing.T) {
	isolateHome(t)

	pinned, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	ports, err := resolvePorts(Options{AlarmAPort: pinned})
	if err != nil {
		t.Fatalf("resolvePorts: %v", err)
	}
	if ports.AlarmA != pinned {
		t.Errorf("alarm-a port = %d, want the pinned %d", ports.AlarmA, pinned)
	}
}

func TestResolvePorts_PinnedPortInUseFailsClosed(t *testing.T) {
	isolateHome(t)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	taken := l.Addr().(*net.TCPAddr).Port

	_, err = resolvePorts(Options{AlarmAPort: taken})
	if err == nil {
		t.Fatalf("resolvePorts accepted a pinned port that is in use (%d)", taken)
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("error = %v, want one that names the port conflict", err)
	}
}

func TestResolvePorts_DuplicatePinsFail(t *testing.T) {
	isolateHome(t)

	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolvePorts(Options{AlarmAPort: port, AlarmBPort: port})
	if err == nil {
		t.Fatalf("resolvePorts accepted the same pin for alarm-a and alarm-b (%d)", port)
	}
}

func TestValidatePins(t *testing.T) {
	isolateHome(t)

	free, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if pinErr := validatePins(Options{AlarmAPort: free}); pinErr != nil {
		t.Errorf("validatePins rejected a free pin: %v", pinErr)
	}
	if pinErr := validatePins(Options{AlarmAPort: free, ADHDPort: free}); pinErr == nil {
		t.Error("validatePins accepted the same port twice")
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	taken := l.Addr().(*net.TCPAddr).Port
	if err := validatePins(Options{AlarmAPort: taken}); err == nil {
		t.Error("validatePins accepted a port that is in use")
	}
}

func TestStateRoundTrip(t *testing.T) {
	isolateHome(t)

	state := State{
		PID:       os.Getpid(),
		Cluster:   ClusterInfo{Name: "demo-test", AlarmA: "http://127.0.0.1:1", AlarmB: "http://127.0.0.1:2", AdhdMCP: "http://127.0.0.1:3/mcp"},
		Ports:     Ports{AlarmA: 1, AlarmB: 2, ADHD: 3},
		Discovery: "http://127.0.0.1:19100/cluster",
		LogDir:    "/tmp/lezz-demo-test",
	}
	if err := writeState(state); err != nil {
		t.Fatalf("writeState: %v", err)
	}

	got, ok := RunningState() // our own pid is alive
	if !ok {
		t.Fatal("RunningState did not report the state we just wrote")
	}
	if got.PID != state.PID || got.Cluster.AlarmA != state.Cluster.AlarmA {
		t.Errorf("read back %+v, want %+v", got, state)
	}

	removeState(state.PID)
	if _, ok := readState(); ok {
		t.Error("removeState left the state file behind")
	}
}

func TestRunningState_RemovesStaleState(t *testing.T) {
	isolateHome(t)

	// A pid that cannot be alive on a normal host.
	if err := writeState(State{PID: 1 << 22, Cluster: ClusterInfo{Name: "stale"}}); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	if _, ok := RunningState(); ok {
		t.Fatal("RunningState reported a stale state as running")
	}
	path, err := stateFile()
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("stale state file was not removed: %v", statErr)
	}
}

func TestStop_RefusesProcessThatIsNotDemo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no command-line probe on Windows")
	}
	isolateHome(t)

	// Our own pid is alive but the test binary's command line does not contain
	// the word "demo", so Stop must refuse rather than signal it.
	if err := writeState(State{PID: os.Getpid(), Cluster: ClusterInfo{Name: "foreign"}}); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	err := Stop(100_000_000) // 100ms in ns, never reached
	if err == nil || !strings.Contains(err.Error(), "does not look like") {
		t.Fatalf("Stop = %v, want a refusal", err)
	}
}

func TestProbe(t *testing.T) {
	isolateHome(t)

	alarm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer alarm.Close()
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer discovery.Close()

	adhdLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adhdLn.Close() }()

	state := State{
		Cluster: ClusterInfo{
			AlarmA:  alarm.URL,
			AlarmB:  alarm.URL,
			AdhdMCP: fmt.Sprintf("http://%s/mcp", adhdLn.Addr().String()),
		},
		Discovery: discovery.URL,
	}
	health := Probe(context.Background(), state)
	if len(health) != 4 {
		t.Fatalf("Probe returned %d checks, want 4", len(health))
	}
	for _, h := range health {
		if !h.OK {
			t.Errorf("%s (%s) reported down: %s", h.Name, h.Endpoint, h.Detail)
		}
	}

	// A dead endpoint is reported, not returned as an error.
	deadPort, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	state.Cluster.AdhdMCP = fmt.Sprintf("http://127.0.0.1:%d/mcp", deadPort)
	health = Probe(context.Background(), state)
	if health[2].Name != "adhd MCP" || health[2].OK {
		t.Errorf("dead adhd endpoint reported as healthy: %+v", health[2])
	}
}

func TestLogPathCreatesLogDir(t *testing.T) {
	home := isolateHome(t)

	path, err := LogPath()
	if err != nil {
		t.Fatalf("LogPath: %v", err)
	}
	want := filepath.Join(home, ".lezz", "logs", "demo.log")
	if path != want {
		t.Errorf("LogPath = %q, want %q", path, want)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		t.Errorf("log dir was not created: %v", err)
	}
}
