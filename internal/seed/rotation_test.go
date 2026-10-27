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
	subject := rotation.Subject()
	if len(subject) != 40 {
		t.Fatalf("a clean committed tree must bind a subject, got %q", subject)
	}
	if got := rotation.String(); !strings.HasPrefix(got, "lezz@7 +1 [lint=+1 git=+1] "+subject[:12]) {
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

// TestIdenticalMarksOverDifferentCommitsAreNotInterchangeable is the tag case:
// a passing lint over a tagged commit and a passing lint over a different commit
// carry identical marks, and only the subject tells them apart.
func TestIdenticalMarksOverDifferentCommitsAreNotInterchangeable(t *testing.T) {
	dir := gitFixture(t, cleanFixture, true)

	first := NewRotation(context.Background(), "lezz", 10, dir)
	if first.Mark() != 1 {
		t.Fatalf("fixture must hold: %s", first)
	}

	// Move HEAD without changing the verdict: an empty commit is a new subject
	// with the same marks.
	commit := exec.Command("git", "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "next", "--allow-empty") //nolint:gosec // fixed argv in a test fixture
	commit.Dir = dir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	second := NewRotation(context.Background(), "lezz", 11, dir)

	if first.Mark() != second.Mark() {
		t.Fatalf("marks differ across an empty commit: %s vs %s", first, second)
	}
	if first.Subject() == second.Subject() || second.Subject() == "" {
		t.Fatalf("subjects did not move with HEAD: %q vs %q", first.Subject(), second.Subject())
	}
}

// TestADirtyTreeHasNoSubject is the failure the tag comparison depends on: a
// lint pass over uncommitted bytes has identical marks to a gated one and no
// subject, so it cannot be cited in its place.
func TestADirtyTreeHasNoSubject(t *testing.T) {
	dir := gitFixture(t, cleanFixture, true)
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rotation := NewRotation(context.Background(), "lezz", 12, dir)
	lint, _ := rotation.Invariant("lint")
	if lint.Mark() != 1 {
		t.Fatalf("the fixture should still lint: %s", lint.Detail)
	}
	if lint.Subject != "" {
		t.Fatalf("a dirty tree bound a subject: %q", lint.Subject)
	}
	if rotation.Subject() != "" {
		t.Fatalf("a dirty rotation reported a subject: %q", rotation.Subject())
	}
	ok, reason := rotation.Accept(true)
	if ok || !strings.Contains(reason, "unbound") {
		t.Fatalf("a dirty lint pass was accepted as a tag's rotation: %v %q", ok, reason)
	}
}

// TestAWorktreeSharesTheSubject is the copy case: a worktree is another
// checkout of one seed, so it is a different workspace over the same subject.
// Its rotation is portable to the original checkout — and a dirty worktree is
// still unbound.
func TestAWorktreeSharesTheSubject(t *testing.T) {
	dir := gitFixture(t, cleanFixture, true)

	worktree := filepath.Join(t.TempDir(), "wt")
	add := exec.Command("git", "worktree", "add", "-q", worktree, "HEAD") //nolint:gosec // fixed argv in a test fixture
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v %s", err, out)
	}

	origin := NewRotation(context.Background(), "lezz", 20, dir)
	copied := NewRotation(context.Background(), "lezz", 20, worktree)

	if origin.Subject() == "" || copied.Subject() == "" {
		t.Fatalf("a clean worktree must bind a subject: %q / %q", origin.Subject(), copied.Subject())
	}
	if origin.Subject() != copied.Subject() {
		t.Fatalf("a worktree is another checkout of one seed, got %q and %q", origin.Subject(), copied.Subject())
	}
	if origin.Mark() != copied.Mark() {
		t.Fatalf("marks differ across a worktree: %s vs %s", origin, copied)
	}

	// The copy's rotation stands for the seed: same subject, same verdict.
	if ok, reason := copied.Accept(true); !ok {
		t.Fatalf("a worktree rotation was refused: %s", reason)
	}
	if ok, reason := origin.Accept(true); !ok {
		t.Fatalf("the seed's rotation was refused: %s", reason)
	}

	// The workspace differs even though the subject does not: a dirty worktree
	// is its own unbound state and cannot borrow the clean one's subject.
	if err := os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty := NewRotation(context.Background(), "lezz", 21, worktree)
	if dirty.Subject() != "" {
		t.Fatalf("a dirty worktree bound a subject: %q", dirty.Subject())
	}
	if ok, _ := dirty.Accept(true); ok {
		t.Fatal("a dirty worktree was accepted as the seed's rotation")
	}
}
