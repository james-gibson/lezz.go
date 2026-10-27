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

	rotation := NewRotation(context.Background(), "lezz", "agent-a", 7, dir)
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
	if got := rotation.String(); !strings.HasPrefix(got, "lezz@7 by agent-a +1 [lint=+1 git=+1] "+subject[:12]) {
		t.Fatalf("rotation rendered as %q", got)
	}
}

// TestLintFailureInvertsOnlyTheLintLayer is the adversarial case, and it checks
// the separation: the tree is committed (git holds), the lint layer fails, and a
// claim of correctness is refused naming lint — while git keeps its own mark.
func TestLintFailureInvertsOnlyTheLintLayer(t *testing.T) {
	dir := gitFixture(t, brokenFixture, true)

	rotation := NewRotation(context.Background(), "lezz", "agent-a", 8, dir)
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

	rotation := NewRotation(context.Background(), "lezz", "agent-a", 9, dir)
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

	first := NewRotation(context.Background(), "lezz", "agent-a", 10, dir)
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
	second := NewRotation(context.Background(), "lezz", "agent-a", 11, dir)

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

	rotation := NewRotation(context.Background(), "lezz", "agent-a", 12, dir)
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

	origin := NewRotation(context.Background(), "lezz", "agent-a", 20, dir)
	copied := NewRotation(context.Background(), "lezz", "agent-a", 20, worktree)

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
	dirty := NewRotation(context.Background(), "lezz", "agent-a", 21, worktree)
	if dirty.Subject() != "" {
		t.Fatalf("a dirty worktree bound a subject: %q", dirty.Subject())
	}
	if ok, _ := dirty.Accept(true); ok {
		t.Fatal("a dirty worktree was accepted as the seed's rotation")
	}
}

// TestPassingClaimantsAreOnlyToldApartByTheirClaimant is the shared-space case:
// several agents work in their own checkouts of one seed, each presuming its own
// tree passes lint. The passing marks are identical — success leaves no trace —
// so the claimant is the only discriminator, and the agent whose lint inverted
// is the only one the evidence can name.
func TestPassingClaimantsAreOnlyToldApartByTheirClaimant(t *testing.T) {
	dir := gitFixture(t, cleanFixture, true)

	// Two more workspaces over the same subject.
	claims := []struct {
		name string
		dir  string
		bad  bool
	}{
		{name: "agent-a", dir: dir},
		{name: "agent-b", dir: worktree(t, dir, "wt-b")},
		{name: "agent-c", dir: worktree(t, dir, "wt-c"), bad: true},
	}
	if claims[2].bad {
		// The failing agent never committed: its work is unbound, not a pass.
		if err := os.WriteFile(filepath.Join(claims[2].dir, "broken.go"), []byte(brokenFixture), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rotations := make([]Rotation, 0, len(claims))
	for _, c := range claims {
		rotations = append(rotations, NewRotation(context.Background(), "lezz", c.name, 30, c.dir))
	}

	// The two clean workspaces share a subject and a mark.
	if rotations[0].Subject() == "" || rotations[0].Subject() != rotations[1].Subject() {
		t.Fatalf("clean workspaces of one seed must share a subject: %q / %q", rotations[0].Subject(), rotations[1].Subject())
	}
	if rotations[0].Mark() != rotations[1].Mark() || rotations[0].Mark() != 1 {
		t.Fatalf("clean workspaces must mark alike: %s vs %s", rotations[0], rotations[1])
	}
	if rotations[0].Claimant == rotations[1].Claimant {
		t.Fatal("fixture: the two claims are the same claimant")
	}

	// The failing agent is the only one the evidence can name, and it cannot
	// borrow the shared subject: its tree is unbound.
	if rotations[2].Mark() != -1 {
		t.Fatalf("the failing agent did not invert: %s", rotations[2])
	}
	if rotations[2].Subject() != "" {
		t.Fatalf("an uncommitted failure bound a subject: %q", rotations[2].Subject())
	}
	ok, reason := rotations[2].Accept(true)
	if ok {
		t.Fatal("the failing agent's claim was accepted")
	}
	if !strings.Contains(reason, "lint") || !strings.Contains(reason, "unbound") {
		t.Fatalf("refusal does not name the layer and the missing binding: %q", reason)
	}

	// An anonymous passing claim could not be placed in any order at all.
	anonymous := NewRotation(context.Background(), "lezz", "", 30, dir)
	if ok, reason := anonymous.Accept(true); ok || !strings.Contains(reason, "claimant") {
		t.Fatalf("an anonymous passing claim was accepted: %v %q", ok, reason)
	}
}

// worktree adds a clean checkout of dir at name and returns its path.
func worktree(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("git", "worktree", "add", "-q", path, "HEAD") //nolint:gosec // fixed argv in a test fixture
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add %s: %v %s", name, err, out)
	}
	return path
}

// TestARotationPreparedLocallyAppliesToTheRemote is the portability case: the
// remote HEAD and the local HEAD are one subject, so a rotation verified in the
// checkout can be prepared for the remote — and stops applying the moment the
// remote moves.
func TestARotationPreparedLocallyAppliesToTheRemote(t *testing.T) {
	dir := gitFixture(t, cleanFixture, true)

	remote := filepath.Join(t.TempDir(), "remote.git")
	init := exec.Command("git", "init", "-q", "--bare", remote) //nolint:gosec // fixed argv in a test fixture
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("bare init: %v %s", err, out)
	}
	push := exec.Command("git", "push", "-q", remote, "HEAD:refs/heads/main") //nolint:gosec // fixed argv in a test fixture
	push.Dir = dir
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("push: %v %s", err, out)
	}

	rotation := NewRotation(context.Background(), "lezz", "agent-a", 40, dir)
	if rotation.Subject() == "" {
		t.Fatalf("no subject: %s", rotation)
	}

	remoteHead := headOf(t, remote, "refs/heads/main")
	if !rotation.Applies(remoteHead) {
		t.Fatalf("a rotation over the local HEAD does not apply to the remote: %q vs %q",
			rotation.Subject(), remoteHead)
	}
	// Prepared for the remote: it stands, and it says who verified it.
	if ok, reason := rotation.Accept(true); !ok {
		t.Fatalf("the prepared rotation was refused: %s", reason)
	}
	lint, _ := rotation.Invariant("lint")
	if lint.Verifier != "local:go-vet" {
		t.Fatalf("portability dropped the verifier: %q", lint.Verifier)
	}

	// The remote moves: the old rotation describes bytes that are no longer there.
	next := exec.Command("git", "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "next", "--allow-empty") //nolint:gosec // fixed argv in a test fixture
	next.Dir = dir
	if out, err := next.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	push = exec.Command("git", "push", "-q", remote, "HEAD:refs/heads/main") //nolint:gosec // fixed argv in a test fixture
	push.Dir = dir
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("push: %v %s", err, out)
	}
	if rotation.Applies(headOf(t, remote, "refs/heads/main")) {
		t.Fatal("a stale rotation applied to a remote that moved")
	}
	if !NewRotation(context.Background(), "lezz", "agent-a", 41, dir).Applies(headOf(t, remote, "refs/heads/main")) {
		t.Fatal("a fresh rotation does not apply to the remote it was prepared from")
	}
}

// headOf reads a ref from a repository.
func headOf(t *testing.T, repo, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref) //nolint:gosec // fixed argv in a test fixture
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}
