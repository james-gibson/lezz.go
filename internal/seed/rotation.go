//
//  rotation.go
//  lezz.go
//
//  A seed rotation that is marked, not asserted.
//
//  The point of this file is small and load-bearing: a rotation's correctness
//  is decided by a tool that can fail, and a failure is recorded in the
//  inverse. Nothing here has to be believed — an agent (or a person) can claim
//  the rotation is correct, and a claim cannot promote a rotation whose mark is
//  inverted. That is the whole mechanism; the lint verdict is the verifier.
//

package seed

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Rotation is one step of a seed's rotation, carrying the mark its verifier
// gave it.
type Rotation struct {
	SeedID string `json:"seed_id"`
	Tick   uint64 `json:"tick"`
	// Lint is the verifier's verdict: true when the tree lints.
	Lint bool `json:"lint"`
	// Detail is the verifier's own words when it failed. It is not a summary
	// written by the caller: the caller cannot mark its own rotation.
	Detail string `json:"detail,omitempty"`
}

// Mark is +1 for a correct rotation and -1 for an inverted one.
//
// The inverse is a distinct state, not "false": a rotation whose verifier
// failed is marked against itself, so anything that only checks "is the mark
// truthy" cannot mistake an inverted rotation for a missing one.
func (r Rotation) Mark() int {
	if r.Lint {
		return 1
	}
	return -1
}

// Accept decides whether a claim about this rotation may be recorded. A claim
// of correctness is accepted only when the mark is positive; a claim over an
// inverted rotation is refused with the verifier's reason, and a claim that the
// rotation is wrong is always accepted (refusing a broken rotation is honest
// whatever the verifier said).
func (r Rotation) Accept(claimCorrect bool) (bool, string) {
	switch {
	case r.Lint && claimCorrect:
		return true, "correct: the tree lints"
	case r.Lint && !claimCorrect:
		return true, "accepted: the claim refuses a rotation that lints"
	case !r.Lint && !claimCorrect:
		return true, "accepted: the rotation is inverted and the claim agrees"
	default:
		reason := "inverted: the tree does not lint"
		if r.Detail != "" {
			reason += ": " + firstLine(r.Detail)
		}
		return false, reason + " — a claim cannot promote an inverted rotation"
	}
}

// NewRotation marks a rotation at seedID/tick by linting dir.
func NewRotation(ctx context.Context, seedID string, tick uint64, dir string) Rotation {
	r := LintDir(ctx, dir)
	r.SeedID = seedID
	r.Tick = tick
	return r
}

// LintDir runs `go vet` in dir and returns the verdict for a rotation. It is the
// verifier, and the seam a test may replace; the default is the real toolchain.
var LintDir = func(ctx context.Context, dir string) Rotation {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "vet", "./...") //nolint:gosec // fixed argv; dir is a lab fixture
	cmd.Dir = dir
	// A workspace above the fixture would pull the module out of isolation, and
	// -mod=mod keeps a stale vendor tree from turning a clean tree into a fail.
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()

	r := Rotation{Lint: err == nil}
	if err != nil {
		r.Detail = strings.TrimSpace(string(out))
		if r.Detail == "" {
			r.Detail = err.Error()
		}
	}
	return r
}

// firstLine keeps a verifier's reason to one line for an error string.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// String renders a rotation as the mark it carries, for a log or a receipt.
func (r Rotation) String() string {
	return fmt.Sprintf("%s@%d %+d", r.SeedID, r.Tick, r.Mark())
}
