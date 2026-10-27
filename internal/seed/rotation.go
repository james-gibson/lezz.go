//
//  rotation.go
//  lezz.go
//
//  A seed rotation that is marked, not asserted.
//
//  The point of this file is small and load-bearing: a rotation's correctness
//  is decided by tools that can fail, and a failure is recorded in the inverse.
//  Nothing here has to be believed — an agent (or a person) can claim the
//  rotation is correct, and a claim cannot promote a rotation whose mark is
//  inverted.
//
//  Each invariant is verified on its own and carries its own mark. A dirty git
//  state is not a lint failure with a different detail message: it is a
//  separate invariant, so a rotation can say which layer broke instead of
//  collapsing every failure into one word. The rotation is as correct as its
//  weakest invariant, and widening the verifier means adding an invariant, not
//  editing this file's arithmetic.
//

package seed

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Invariant names one property a rotation asserts, and the verifier's verdict on
// it.
type Invariant struct {
	// Name is the layer: "lint", "git", and whatever is added next.
	Name string `json:"name"`
	// Pass is the verifier's verdict. The caller never sets it.
	Pass bool `json:"pass"`
	// Detail is the verifier's own words when it failed.
	Detail string `json:"detail,omitempty"`
}

// Mark is +1 when the invariant holds and -1 when it is inverted.
//
// The inverse is a distinct state, not "false": an invariant whose verifier
// failed is marked against itself, so anything that only checks "is the mark
// truthy" cannot mistake an inverted invariant for a missing one.
func (i Invariant) Mark() int {
	if i.Pass {
		return 1
	}
	return -1
}

// Accept decides whether a claim about this invariant may be recorded. A claim
// that it holds is accepted only when the mark is positive; a claim over an
// inverted invariant is refused with the verifier's reason. A claim that it is
// broken is always accepted — refusing a broken invariant is honest whatever
// the verifier said.
func (i Invariant) Accept(claimHolds bool) (bool, string) {
	name := i.Name
	if name == "" {
		name = "invariant"
	}
	switch {
	case i.Pass && claimHolds:
		return true, name + ": correct"
	case !i.Pass && !claimHolds:
		return true, name + ": the claim agrees it is inverted"
	case i.Pass && !claimHolds:
		return true, name + ": accepted, the claim refuses an invariant that holds"
	default:
		reason := name + " is inverted"
		if i.Detail != "" {
			reason += ": " + firstLine(i.Detail)
		}
		return false, reason + " — a claim cannot promote an inverted invariant"
	}
}

// Rotation is one step of a seed's rotation and the verdict on every invariant
// it asserts.
type Rotation struct {
	SeedID     string      `json:"seed_id"`
	Tick       uint64      `json:"tick"`
	Invariants []Invariant `json:"invariants"`
}

// Mark is the weakest invariant's mark: +1 only when every layer holds, -1 as
// soon as one is inverted. A rotation is as correct as its weakest layer.
func (r Rotation) Mark() int {
	if len(r.Invariants) == 0 {
		return -1 // nothing verified is not a pass
	}
	mark := 1
	for _, inv := range r.Invariants {
		if inv.Mark() < mark {
			mark = inv.Mark()
		}
	}
	return mark
}

// Accept decides whether a claim about the whole rotation may be recorded. A
// single inverted layer refuses it, and the reason names the layer.
func (r Rotation) Accept(claimCorrect bool) (bool, string) {
	if !claimCorrect {
		for _, inv := range r.Invariants {
			if !inv.Pass {
				return true, inv.Name + ": accepted, the rotation is inverted"
			}
		}
		if r.Mark() == 1 {
			return true, "accepted: the claim refuses a rotation that holds"
		}
		return true, "accepted: the claim refuses a rotation with no verified layer"
	}
	for _, inv := range r.Invariants {
		if ok, reason := inv.Accept(true); !ok {
			return false, reason
		}
	}
	if r.Mark() != 1 {
		return false, "refused: no invariant was verified"
	}
	return true, "correct: every invariant holds"
}

// Invariant returns the named layer's verdict.
func (r Rotation) Invariant(name string) (Invariant, bool) {
	for _, inv := range r.Invariants {
		if inv.Name == name {
			return inv, true
		}
	}
	return Invariant{}, false
}

// String renders a rotation as its aggregate mark and each layer's mark.
func (r Rotation) String() string {
	parts := make([]string, 0, len(r.Invariants))
	for _, inv := range r.Invariants {
		parts = append(parts, fmt.Sprintf("%s=%+d", inv.Name, inv.Mark()))
	}
	return fmt.Sprintf("%s@%d %+d [%s]", r.SeedID, r.Tick, r.Mark(), strings.Join(parts, " "))
}

// NewRotation marks a rotation at seedID/tick by verifying every layer in dir.
// It widens one layer at a time: today lint and cleanliness, each on its own.
func NewRotation(ctx context.Context, seedID string, tick uint64, dir string) Rotation {
	return Rotation{
		SeedID: seedID,
		Tick:   tick,
		Invariants: []Invariant{
			LintDir(ctx, dir),
			GitCleanDir(ctx, dir),
		},
	}
}

// LintDir runs `go vet` in dir. It is a verifier and a seam; the default is the
// real toolchain.
var LintDir = func(ctx context.Context, dir string) Invariant {
	out, err := run(ctx, dir, "go", "vet", "./...")
	inv := Invariant{Name: "lint", Pass: err == nil}
	if err != nil {
		inv.Detail = detailOr(out, err)
	}
	return inv
}

// GitCleanDir runs `git status --porcelain` in dir. An empty result is clean.
//
// A directory that is not a repository is inverted, not skipped: an invariant
// that cannot be checked has not been shown to hold, and treating "unknown" as
// "clean" is how a dirty state gets recorded as a rotation.
var GitCleanDir = func(ctx context.Context, dir string) Invariant {
	out, err := run(ctx, dir, "git", "status", "--porcelain")
	if err != nil {
		return Invariant{Name: "git", Detail: detailOr(out, err)}
	}
	if dirty := strings.TrimSpace(string(out)); dirty != "" {
		return Invariant{Name: "git", Detail: firstLine(dirty)}
	}
	return Invariant{Name: "git", Pass: true}
}

// run executes one verifier in dir with a bounded clock and no workspace
// leakage, and returns its combined output.
func run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed argv; dir is a lab fixture
	cmd.Dir = dir
	// GOWORK=off keeps a workspace above a fixture from pulling it out of
	// isolation; -mod=mod keeps a stale vendor tree from failing a clean one.
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = bytes.NewReader(nil)

	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

// detailOr keeps a verifier's reason to something a log can carry.
func detailOr(out []byte, err error) string {
	if text := strings.TrimSpace(string(out)); text != "" {
		return text
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// firstLine keeps a verifier's reason to one line for an error string.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
