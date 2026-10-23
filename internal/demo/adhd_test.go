package demo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeADHD writes a stand-in adhd that mirrors the flag package: it exits 2
// with "flag provided but not defined" for each rejected flag, and otherwise
// prints its version. It is the shape of the real failure in #28, where the
// installed v0.8.6 rejected --force before doing anything else.
func fakeADHD(t *testing.T, version string, reject ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in is a shell script")
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, flag := range reject {
		fmt.Fprintf(&b, "for arg in \"$@\"; do\n  if [ \"$arg\" = %q ]; then\n    echo \"flag provided but not defined: -%s\" >&2\n    exit 2\n  fi\ndone\n",
			flag, strings.TrimLeft(flag, "-"))
	}
	fmt.Fprintf(&b, "echo %q\nexit 0\n", version)

	path := filepath.Join(t.TempDir(), "adhd")
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil { //nolint:gosec // a test binary we just wrote
		t.Fatalf("write fake adhd: %v", err)
	}
	return path
}

func TestPlanADHD_AcceptsTheCommandLineAsIs(t *testing.T) {
	binary := fakeADHD(t, "adhd v0.9.0")

	args := []string{"--headless", "--mcp-addr", ":9090", adhdForce}
	planned, notes, err := planADHD(binary, args)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if strings.Join(planned, " ") != strings.Join(args, " ") {
		t.Fatalf("planned %q, want %q", planned, args)
	}
}

// TestPlanADHD_DropsForceForAReleaseThatPredatesIt is the live trigger in #28:
// the newest published adhd has no --force, so the demo must not pass it — and
// must say that it is not passing it.
func TestPlanADHD_DropsForceForAReleaseThatPredatesIt(t *testing.T) {
	binary := fakeADHD(t, "adhd v0.8.6", adhdForce)

	args := []string{"--headless", "--mcp-addr", ":9090", adhdForce}
	planned, notes, err := planADHD(binary, args)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, a := range planned {
		if a == adhdForce {
			t.Fatalf("planned %q still passes %s", planned, adhdForce)
		}
	}
	if len(planned) != len(args)-1 {
		t.Fatalf("planned %q, want the command line without %s", planned, adhdForce)
	}
	if len(notes) != 1 {
		t.Fatalf("want one note, got %v", notes)
	}
	if !strings.Contains(notes[0], "v0.8.6") || !strings.Contains(notes[0], adhdForce) {
		t.Fatalf("note %q does not name the version and the dropped flag", notes[0])
	}
}

// TestPlanADHD_FailsFastOnAnIncompatibleBinary: a flag the demo cannot drop is
// a hard stop before the cluster starts, carrying adhd's own complaint and the
// upgrade, rather than a 15 s readiness timeout.
func TestPlanADHD_FailsFastOnAnIncompatibleBinary(t *testing.T) {
	binary := fakeADHD(t, "adhd v0.1.0", "--mcp-addr")

	planned, _, err := planADHD(binary, []string{"--headless", "--mcp-addr", ":9090", adhdForce})
	if err == nil {
		t.Fatalf("expected a compatibility error, planned %q", planned)
	}
	for _, want := range []string{"flag provided but not defined: -mcp-addr", "lezz tools upgrade adhd"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestPlanADHD_MissingBinary(t *testing.T) {
	_, _, err := planADHD(filepath.Join(t.TempDir(), "absent"), []string{"--headless"})
	if err == nil {
		t.Fatal("expected an error for a binary that does not run")
	}
	if !strings.Contains(err.Error(), "lezz tools upgrade adhd") {
		t.Fatalf("error %q does not carry the fix", err)
	}
}

func TestStripFlag(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    []string
		removed bool
	}{
		{name: "absent", args: []string{"--headless"}, want: []string{"--headless"}},
		{name: "present", args: []string{"--headless", "--force"}, want: []string{"--headless"}, removed: true},
		{name: "twice", args: []string{"--force", "--headless", "--force"}, want: []string{"--headless"}, removed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, removed := stripFlag(tc.args, "--force")
			if removed != tc.removed || strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("stripFlag(%q) = (%q, %v), want (%q, %v)", tc.args, got, removed, tc.want, tc.removed)
			}
		})
	}
}

// TestWaitServiceOpen_FailsAsSoonAsTheChildDoes: the child's exit is reported
// immediately instead of waiting out the timeout.
func TestWaitServiceOpen_FailsAsSoonAsTheChildDoes(t *testing.T) {
	exited := make(chan error, 1)
	exited <- errors.New("exit status 2")

	start := time.Now()
	err := waitServiceOpen(context.Background(), "127.0.0.1:1", exited)
	if err == nil {
		t.Fatal("expected the child's exit to fail readiness")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %s; the exit should be reported at once", elapsed)
	}
	for _, want := range []string{"exited during startup", "exit status 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestReadinessError_CarriesTheChildsOwnWords is the legibility half of #28: the
// operator who sees "adhd MCP readiness" must also see why, and where the full
// log went.
func TestReadinessError_CarriesTheChildsOwnWords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logPath := writeLog(t, "flag provided but not defined: -force\nUsage of adhd:\n")

	err := readinessError("adhd MCP", errors.New("exited during startup (exit status 2)"), logPath)
	for _, want := range []string{
		"adhd MCP readiness",
		"exited during startup (exit status 2)",
		"flag provided but not defined: -force",
		"full log: ",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	// The evidence must be attached once, not once per wrapper.
	if n := strings.Count(err.Error(), "full log: "); n != 1 {
		t.Fatalf("log evidence appears %d times in %q", n, err)
	}
}

func TestWaitServiceOpen_Opens(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if waitErr := waitServiceOpen(context.Background(), ln.Addr().String(), nil); waitErr != nil {
		t.Fatalf("open port reported not ready: %v", waitErr)
	}
}

func TestWaitServiceOpen_Timeout(t *testing.T) {
	old := readyTimeout
	readyTimeout = 100 * time.Millisecond
	defer func() { readyTimeout = old }()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	waitErr := waitServiceOpen(context.Background(), addr, nil)
	if waitErr == nil {
		t.Fatal("expected a timeout")
	}
	for _, want := range []string{"timed out after", addr} {
		if !strings.Contains(waitErr.Error(), want) {
			t.Fatalf("error %q does not mention %q", waitErr, want)
		}
	}
}

// TestLogEvidence_SurvivesTheTempRoot is the second half of #28: the demo
// removes its temp root on the way out, so the evidence has to be copied first.
func TestLogEvidence_SurvivesTheTempRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	logPath := writeLog(t, "line one\nline two\n")

	evidence := logEvidence(logPath)
	kept := filepath.Join(home, ".lezz", "logs", "demo-"+filepath.Base(logPath))
	if !strings.Contains(evidence, kept) {
		t.Fatalf("evidence %q does not name the kept log %s", evidence, kept)
	}
	for _, want := range []string{"line one", "line two"} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("evidence %q does not include %q", evidence, want)
		}
	}

	// The copy outlives the original, which the demo's deferred cleanup deletes.
	if err := os.Remove(logPath); err != nil {
		t.Fatalf("remove original: %v", err)
	}
	data, err := os.ReadFile(kept) //nolint:gosec // path is under the test's temp HOME
	if err != nil {
		t.Fatalf("kept log: %v", err)
	}
	if string(data) != "line one\nline two\n" {
		t.Fatalf("kept log = %q", data)
	}
}

func TestLogEvidence_NoOutput(t *testing.T) {
	if got := logEvidence(filepath.Join(t.TempDir(), "absent.log")); got != "the service wrote no output" {
		t.Fatalf("logEvidence(absent) = %q", got)
	}
}

// writeLog writes a child's log into a temp dir shaped like the demo's.
func writeLog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adhd.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return path
}
