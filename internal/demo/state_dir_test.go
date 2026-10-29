package demo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestResolveStateDirPrecedence: the flag wins, then the environment, then the
// historical default. Whatever wins is made absolute and created, so a detached
// child writing from the same working directory lands on the same files.
func TestResolveStateDirPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(StateDirEnv, "")

	def, err := ResolveStateDir("")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if want := filepath.Join(home, ".lezz"); def != want {
		t.Fatalf("default state dir = %q, want %q", def, want)
	}
	if info, statErr := os.Stat(def); statErr != nil || !info.IsDir() {
		t.Fatalf("default state dir was not created: %v", statErr)
	}

	fromEnv := filepath.Join(t.TempDir(), "cluster-b")
	t.Setenv(StateDirEnv, fromEnv)
	got, err := ResolveStateDir("")
	if err != nil {
		t.Fatalf("resolve from env: %v", err)
	}
	if got != fromEnv {
		t.Fatalf("env state dir = %q, want %q", got, fromEnv)
	}

	fromFlag := filepath.Join(t.TempDir(), "cluster-a")
	got, err = ResolveStateDir(fromFlag)
	if err != nil {
		t.Fatalf("resolve from flag: %v", err)
	}
	if got != fromFlag {
		t.Fatalf("flag state dir = %q, want %q (the flag must beat the env)", got, fromFlag)
	}
}

// TestResolveStateDirIsAbsolute: a relative --state-dir is resolved once, in the
// parent, so the detached child cannot write somewhere else.
func TestResolveStateDirIsAbsolute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got, err := ResolveStateDir("relative-state")
	if err != nil {
		t.Fatalf("resolve relative: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("state dir = %q, want an absolute path", got)
	}
	if filepath.Base(got) != "relative-state" {
		t.Fatalf("state dir = %q, want it to end in the given name", got)
	}
}

// TestTwoClustersShareOneHome is the acceptance test for lezz.go#60: two
// clusters start under the same real HOME, each with its own --state-dir, and
// `status|stop` reach the right one.
//
// It runs real processes: the point of the issue is that a cluster needs the
// operator's actual HOME (~/.config/gh, git config, credentials), and two
// in-process clusters would share a pid — they would publish the same
// demo-<pid> name and prove nothing about targeting.
func TestTwoClustersShareOneHome(t *testing.T) {
	if testing.Short() {
		t.Skip("builds lezz and starts two clusters")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("building lezz needs the go command: %v", err)
	}
	if !listeningAllInterfacesFree(DiscoveryPort) {
		t.Skipf("discovery port %d is already in use on this host", DiscoveryPort)
	}

	home := t.TempDir()
	binDir := filepath.Join(home, ".lezz", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	// The clusters get the real HOME; only their state moves. adhd is a stand-in
	// built here, because CI installs none.
	buildStandInADHD(t, filepath.Join(binDir, "adhd"))
	lezz := buildLezzBinary(t, filepath.Join(home, "lezz"))

	env := append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
	runLezz := func(args ...string) (string, error) {
		cmd := exec.Command(lezz, args...) //nolint:gosec // the binary this test just built
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	dirA := filepath.Join(t.TempDir(), "cluster-a")
	dirB := filepath.Join(t.TempDir(), "cluster-b")
	// Best effort: a failed assertion must not leave clusters running.
	t.Cleanup(func() {
		_, _ = runLezz("demo", "stop", "--state-dir", dirB)
		_, _ = runLezz("demo", "stop", "--state-dir", dirA)
	})

	for _, dir := range []string{dirA, dirB} {
		if out, err := runLezz("demo", "start", "--state-dir", dir); err != nil {
			t.Fatalf("demo start --state-dir %s: %v\n%s", dir, err, out)
		}
	}

	stateA := readStateOrFail(t, dirA)
	stateB := readStateOrFail(t, dirB)

	// Each cluster wrote its state in its own directory, and nowhere else.
	if stateA.StateDir != dirA || stateB.StateDir != dirB {
		t.Fatalf("state dirs = %q/%q, want %q/%q", stateA.StateDir, stateB.StateDir, dirA, dirB)
	}
	if stateA.PID == stateB.PID {
		t.Fatalf("both clusters report pid %d; they are not separate processes", stateA.PID)
	}
	if stateA.Cluster.Name == stateB.Cluster.Name {
		t.Fatalf("both clusters are named %q; a registry cannot tell them apart", stateA.Cluster.Name)
	}
	for _, pair := range [][2]int{
		{stateA.Ports.AlarmA, stateB.Ports.AlarmA},
		{stateA.Ports.AlarmB, stateB.Ports.AlarmB},
		{stateA.Ports.ADHD, stateB.Ports.ADHD},
	} {
		if pair[0] == pair[1] {
			t.Fatalf("clusters share a port: %v", pair)
		}
	}

	// The registry is one directory, and it carries both clusters.
	if clusters := getClusters(t, fmt.Sprintf("http://127.0.0.1:%d", DiscoveryPort)); len(clusters) < 2 {
		t.Fatalf("registry has %d clusters, want both: %v", len(clusters), clusters)
	}

	// status reads the directory it is pointed at, not the default one.
	outA, err := runLezz("demo", "status", "--state-dir", dirA)
	if err != nil {
		t.Fatalf("demo status --state-dir %s: %v\n%s", dirA, err, outA)
	}
	if !strings.Contains(outA, strconv.Itoa(stateA.PID)) {
		t.Errorf("status of cluster A does not report pid %d:\n%s", stateA.PID, outA)
	}
	if !strings.Contains(outA, stateA.Cluster.AdhdMCP) {
		t.Errorf("status of cluster A does not report its own endpoints %q:\n%s", stateA.Cluster.AdhdMCP, outA)
	}

	// stop reaches cluster A only: B keeps running, and its state is untouched.
	if out, stopErr := runLezz("demo", "stop", "--state-dir", dirA); stopErr != nil {
		t.Fatalf("demo stop --state-dir %s: %v\n%s", dirA, stopErr, out)
	}
	if _, still := RunningState(dirA); still {
		t.Fatal("cluster A is still recorded as running after its stop")
	}
	// A stopped cluster leaves the registry too. Its entry lingering is how the
	// directory collects phantom clusters: a stop that arrives before the
	// signal handler is installed kills the process before it can deregister.
	if clusters := getClustersMaybe(t, fmt.Sprintf("http://127.0.0.1:%d", DiscoveryPort)); clusters != nil {
		if _, listed := clusters[stateA.Cluster.Name]; listed {
			t.Errorf("cluster A (%s) is still listed after its stop: %v", stateA.Cluster.Name, clusters)
		}
	}
	if _, alive := RunningState(dirB); !alive {
		t.Fatal("stopping cluster A also stopped cluster B")
	}
	if out, statusErr := runLezz("demo", "status", "--state-dir", dirB); statusErr != nil {
		t.Fatalf("cluster B status after A stopped: %v\n%s", statusErr, out)
	}
	if out, stopErr := runLezz("demo", "stop", "--state-dir", dirB); stopErr != nil {
		t.Fatalf("demo stop --state-dir %s: %v\n%s", dirB, stopErr, out)
	}
	if clusters := getClustersMaybe(t, fmt.Sprintf("http://127.0.0.1:%d", DiscoveryPort)); clusters != nil {
		if _, listed := clusters[stateB.Cluster.Name]; listed {
			t.Errorf("cluster B (%s) is still listed after its stop: %v", stateB.Cluster.Name, clusters)
		}
	}
}

// readStateOrFail waits briefly for a cluster to publish its state, then reads
// it from dir.
func readStateOrFail(t *testing.T, dir string) State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if state, ok := readState(dir); ok {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("no state recorded in %s", dir)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// buildLezzBinary builds the real lezz command, so a test can drive
// `demo start|status|stop` as separate processes under one HOME.
func buildLezzBinary(t *testing.T, dest string) string {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", dest, "github.com/james-gibson/lezz.go/cmd/lezz")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build lezz: %v\n%s", err, out)
	}
	return dest
}

// TestDemoStateDirEnvReachesTheChild: LEZZ_DEMO_STATE_DIR is enough on its own,
// including for `demo stop`, so a shell can export it once instead of repeating
// the flag.
func TestDemoStateDirEnvReachesTheChild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds lezz and starts a cluster")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("building lezz needs the go command: %v", err)
	}
	if !listeningAllInterfacesFree(DiscoveryPort) {
		t.Skipf("discovery port %d is already in use on this host", DiscoveryPort)
	}

	home := t.TempDir()
	binDir := filepath.Join(home, ".lezz", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	buildStandInADHD(t, filepath.Join(binDir, "adhd"))
	lezz := buildLezzBinary(t, filepath.Join(home, "lezz"))

	stateDir := filepath.Join(t.TempDir(), "from-env")
	env := append(os.Environ(), "HOME="+home, "USERPROFILE="+home, StateDirEnv+"="+stateDir)
	runLezz := func(args ...string) (string, error) {
		cmd := exec.Command(lezz, args...) //nolint:gosec // the binary this test just built
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	t.Cleanup(func() { _, _ = runLezz("demo", "stop") })

	if out, err := runLezz("demo", "start"); err != nil {
		t.Fatalf("demo start with %s: %v\n%s", StateDirEnv, err, out)
	}
	if _, ok := readState(stateDir); !ok {
		t.Fatalf("no state in %s; the env var did not reach the child", stateDir)
	}
	if out, err := runLezz("demo", "stop"); err != nil {
		t.Fatalf("demo stop with %s: %v\n%s", StateDirEnv, err, out)
	}
}
