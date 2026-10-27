package seed

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const cleanFixture = `package fixture

// Add is deliberately trivial: the fixture's job is to lint, not to compute.
func Add(a, b int) int { return a + b }
`

const brokenFixture = `package fixture

import "fmt"

func Broken() { fmt.Printf("%d\n", "not a number") }
`

// TestRotationHoldsWhenEveryLayerHolds is the positive case: both invariants
// pass, so the rotation marks +1 and a claim of correctness is accepted.
func TestRotationHoldsWhenEveryLayerHolds(t *testing.T) {
	dir := gitFixture(t, cleanFixture, true)

	rotation := NewRotation(context.Background(), "lezz", 7, dir)
	if rotation.Mark() != 1 {
		t.Fatalf("a clean tree must mark +1, got %+d (%s)", rotation.Mark(), rotation)
	}
	ok, reason := rotation.Accept(true)
	if !ok {
		t.Fatalf("a correct rotation was refused: %s", reason)
	}
	if got := rotation.String(); got != "lezz@7 +1 [lint=+1 git=+1]" {
		t.Fatalf("rotation rendered as %q", got)
	}
}

// TestLintFailureInvertsOnlyTheLintLayer is the adversarial case, and it checks
// the separation: the tree is committed (git holds), the lint layer fails, and a
// claim of correctness is refused naming lint — while git keeps its own mark.
func TestLintFailureInvertsOnlyTheLintLayer(t *testing.T) {
	dir := gitFixture(t, brokenFixture, true)

	rotation := NewRotation(context.Background(), "lezz", 8, dir)
	if rotation.Mark() != -1 {
		t.Fatalf("a failing tree must mark -1, got %+d", rotation.Mark())
	}
	lint, _ := rotation.Invariant("lint")
	git, _ := rotation.Invariant("git")
	if lint.Mark() != -1 {
		t.Fatalf("lint is not inverted: %s", lint.Detail)
	}
	if git.Mark() != 1 {
		t.Fatalf("a committed tree inverted git too: %s", git.Detail)
	}
	t.Logf("lint inverted in isolation: %q", firstLine(lint.Detail))

	// The lie: "yes, the rotation is correct."
	ok, reason := rotation.Accept(true)
	if ok {
		t.Fatalf("a claim promoted an inverted rotation: %s", reason)
	}
	if !strings.Contains(reason, "lint") || !strings.Contains(reason, "inverted") {
		t.Fatalf("refusal does not name the inverted layer: %q", reason)
	}

	// The honest claim over a broken rotation is still accepted.
	if ok, reason := rotation.Accept(false); !ok {
		t.Fatalf("refusing a broken rotation must be accepted: %s", reason)
	}
}

// TestDirtyTreeInvertsOnlyTheGitLayer is the other direction: the code lints,
// the working tree is dirty, and only git is inverted. A dirty tree is its own
// invariant, not a lint failure with a different message.
func TestDirtyTreeInvertsOnlyTheGitLayer(t *testing.T) {
	dir := gitFixture(t, cleanFixture, true)
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rotation := NewRotation(context.Background(), "lezz", 9, dir)
	lint, _ := rotation.Invariant("lint")
	git, _ := rotation.Invariant("git")
	if lint.Mark() != 1 {
		t.Fatalf("a dirty tree inverted lint: %s", lint.Detail)
	}
	if git.Mark() != -1 {
		t.Fatal("a dirty tree did not invert git")
	}
	if rotation.Mark() != -1 {
		t.Fatalf("the rotation is as correct as its weakest layer, got %+d", rotation.Mark())
	}
	ok, reason := rotation.Accept(true)
	if ok || !strings.Contains(reason, "git") {
		t.Fatalf("claim over a dirty tree was accepted, or did not name git: %v %q", ok, reason)
	}
}

// TestUnverifiableLayerIsInverted pins fail-closed: a directory that is not a
// repository has not been shown clean, and unknown is not clean.
func TestUnverifiableLayerIsInverted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(cleanFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	git := GitCleanDir(context.Background(), dir)
	if git.Mark() != -1 {
		t.Fatal("a directory outside git was reported clean")
	}
	if git.Detail == "" {
		t.Fatal("the verifier's reason was dropped")
	}
}

// TestNoVerifiedLayerIsNotAPass pins that an empty rotation is not correct by
// default.
func TestNoVerifiedLayerIsNotAPass(t *testing.T) {
	if mark := (Rotation{SeedID: "lezz", Tick: 1}).Mark(); mark != -1 {
		t.Fatalf("an unverified rotation marked %+d", mark)
	}
}

// gitFixture writes a Go module and commits it, so the cleanliness invariant
// has a baseline that is genuinely clean.
func gitFixture(t *testing.T, source string, commit bool) string {
	t.Helper()
	for _, tool := range []string{"go", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if !commit {
		return dir
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"},
	} {
		cmd := exec.Command("git", args...) //nolint:gosec // fixed argv in a test fixture
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}
