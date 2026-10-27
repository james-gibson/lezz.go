package seed

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotationIsCorrectWhenTheTreeLints is the positive case: the verifier
// passes, the mark is positive, and a claim of correctness is accepted.
func TestRotationIsCorrectWhenTheTreeLints(t *testing.T) {
	dir := lintFixture(t, `package fixture

// Add is deliberately trivial: the fixture's job is to lint, not to compute.
func Add(a, b int) int { return a + b }
`)

	rotation := NewRotation(context.Background(), "lezz", 7, dir)
	if rotation.Mark() != 1 {
		t.Fatalf("a clean tree must mark +1, got %+d (%s)", rotation.Mark(), rotation.Detail)
	}
	if ok, reason := rotation.Accept(true); !ok {
		t.Fatalf("a correct rotation was refused: %s", reason)
	}
	if got := rotation.String(); got != "lezz@7 +1" {
		t.Fatalf("rotation rendered as %q", got)
	}
}

// TestLintFailureIsMarkedInTheInverse is the whole point, and it is written as
// the adversarial case: the tree gains a lint failure, an agent is asked
// whether the rotation is correct, and the agent says yes. The claim is
// refused, because the mark is inverse and a claim cannot promote it.
func TestLintFailureIsMarkedInTheInverse(t *testing.T) {
	dir := lintFixture(t, `package fixture

func Add(a, b int) int { return a + b }
`)
	if rotation := NewRotation(context.Background(), "lezz", 8, dir); rotation.Mark() != 1 {
		t.Fatalf("fixture must start clean, got %+d (%s)", rotation.Mark(), rotation.Detail)
	}

	// Add a lint failure — a printf verb that cannot take its argument.
	bad := filepath.Join(dir, "bad.go")
	if err := os.WriteFile(bad, []byte(`package fixture

import "fmt"

func Broken() { fmt.Printf("%d\n", "not a number") }
`), 0o600); err != nil {
		t.Fatal(err)
	}

	rotation := NewRotation(context.Background(), "lezz", 9, dir)
	if rotation.Mark() != -1 {
		t.Fatalf("a failing tree must mark -1, got %+d", rotation.Mark())
	}
	if rotation.Detail == "" {
		t.Fatal("the verifier's reason was dropped; the mark has no words")
	}
	t.Logf("after the injected failure: mark=%+d reason=%q", rotation.Mark(), firstLine(rotation.Detail))

	// The lie: "yes, the rotation is correct."
	ok, reason := rotation.Accept(true)
	if ok {
		t.Fatalf("a claim of correctness promoted an inverted rotation: %s", reason)
	}
	if !strings.Contains(reason, "inverted") {
		t.Fatalf("refusal does not name the inverse: %q", reason)
	}

	// The honest claim over a broken rotation is still accepted.
	if ok, reason := rotation.Accept(false); !ok {
		t.Fatalf("refusing a broken rotation must be accepted: %s", reason)
	}
}

// TestInvertedIsNotTheSameAsMissing pins that the inverse is a distinct state:
// a caller checking only for a non-zero mark cannot tell it from a pass.
func TestInvertedIsNotTheSameAsMissing(t *testing.T) {
	correct := Rotation{Lint: true}
	inverted := Rotation{Lint: false, Detail: "boom"}

	if correct.Mark() == inverted.Mark() {
		t.Fatal("a pass and an inversion share a mark")
	}
	if correct.Mark() != 1 || inverted.Mark() != -1 {
		t.Fatalf("marks are %+d and %+d, want +1 and -1", correct.Mark(), inverted.Mark())
	}
}

// lintFixture writes a throwaway Go module that can be vetted without a network
// and without escaping into the repository's own workspace.
func lintFixture(t *testing.T, source string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
