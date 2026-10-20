package workspaceagent

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFakeModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	agent := filepath.Join(root, "lab", "agent")
	cmd := filepath.Join(agent, "cmd", "workspace-agent")
	if err := os.MkdirAll(cmd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "go.mod"), []byte("module example.com/fakeagent\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := "package main\n\nfunc main() { println(\"workspace-agent-test\") }\n"
	if err := os.WriteFile(filepath.Join(cmd, "main.go"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFindModuleRepoLayout(t *testing.T) {
	root := writeFakeModule(t)
	got, err := findModule(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(root, "lab", "agent") {
		t.Fatalf("module = %q", got)
	}
}

func TestFindModuleRejectsMissing(t *testing.T) {
	if _, err := findModule(t.TempDir()); err == nil {
		t.Fatal("missing module accepted")
	}
}

func TestBuildLocalModule(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture is a Unix binary")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	root := writeFakeModule(t)
	out := filepath.Join(t.TempDir(), "workspace-agent")
	got, err := Build(context.Background(), Options{
		Source: root,
		Out:    out,
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != out {
		t.Fatalf("out = %q, want %q", got, out)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("binary is not executable: %o", info.Mode().Perm())
	}
	run, err := exec.Command(out).CombinedOutput()
	if err != nil {
		t.Fatalf("run built binary: %v\n%s", err, run)
	}
	if string(run) != "workspace-agent-test\n" {
		t.Fatalf("output = %q", run)
	}
}

func TestEnsureReturnsExisting(t *testing.T) {
	out := filepath.Join(t.TempDir(), "workspace-agent")
	if err := os.WriteFile(out, []byte("already here"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Ensure(context.Background(), Options{Out: out})
	if err != nil {
		t.Fatal(err)
	}
	if got != out {
		t.Fatalf("out = %q", got)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "already here" {
		t.Fatalf("Ensure rebuilt existing binary: %q", data)
	}
}

func TestBuildMissingSource(t *testing.T) {
	_, err := Build(context.Background(), Options{
		Source: filepath.Join(t.TempDir(), "missing"),
		Out:    filepath.Join(t.TempDir(), "workspace-agent"),
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("missing source accepted")
	}
}

func TestDiscoverSourceEnv(t *testing.T) {
	root := writeFakeModule(t)
	t.Setenv("LEZZ_WORKSPACE_AGENT_SRC", root)
	if got := DiscoverSource(); got != root {
		t.Fatalf("source = %q, want %q", got, root)
	}
}

func TestDiscoverSourceCwd(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "lab-safety")
	agent := filepath.Join(src, "lab", "agent")
	if err := os.MkdirAll(filepath.Join(agent, "cmd", "workspace-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "go.mod"), []byte("module example.com/fakeagent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("LEZZ_WORKSPACE_AGENT_SRC", "")
	if got := DiscoverSource(); got != src {
		t.Fatalf("source = %q, want %q", got, src)
	}
}

func TestBuildDiscoversLocalSource(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	root := writeFakeModule(t)
	t.Setenv("LEZZ_WORKSPACE_AGENT_SRC", root)
	out := filepath.Join(t.TempDir(), "workspace-agent")
	if _, err := Build(context.Background(), Options{
		Out:    out,
		Stdout: io.Discard,
		Stderr: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}
