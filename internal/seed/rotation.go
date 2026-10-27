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
	// Subject is the commit the verdict is about. Empty means unbound: the
	// verdict was taken over something a tag cannot name, so it cannot stand in
	// for a tag's rotation even when the mark agrees.
	Subject string `json:"subject,omitempty"`
	// Verifier names the tool that decided Pass, so a local run and a gated
	// one are distinguishable even when their marks are identical.
	Verifier string `json:"verifier,omitempty"`
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
	if claimHolds && i.Subject == "" {
		return false, name + " is unbound: the verdict names no commit, so it cannot stand for a tag"
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
	// Report every refusal, not just the first: a dirty tree and an unattributable
	// lint are different problems, and naming both is what stops one from hiding
	// the other.
	var refusals []string
	for _, inv := range r.Invariants {
		if ok, reason := inv.Accept(true); !ok {
			refusals = append(refusals, reason)
		}
	}
	if len(refusals) > 0 {
		return false, strings.Join(refusals, "; ")
	}
	if r.Mark() != 1 {
		return false, "refused: no invariant was verified"
	}
	return true, "correct: every invariant holds"
}

// Subject is the commit every invariant was verified over. It is empty when the
// layers disagree or when no verdict is bound, which is the state a tag cannot
// be compared against.
func (r Rotation) Subject() string {
	subject := ""
	for _, inv := range r.Invariants {
		if inv.Subject == "" {
			return ""
		}
		if subject == "" {
			subject = inv.Subject
			continue
		}
		if inv.Subject != subject {
			return ""
		}
	}
	return subject
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
	subject := r.Subject()
	if subject == "" {
		subject = "unbound"
	}
	return fmt.Sprintf("%s@%d %+d [%s] %s", r.SeedID, r.Tick, r.Mark(), strings.Join(parts, " "), shortSubject(subject))
}

// NewRotation marks a rotation at seedID/tick by verifying every layer in dir.
// It widens one layer at a time: today lint and cleanliness, each on its own.
func NewRotation(ctx context.Context, seedID string, tick uint64, dir string) Rotation {
	// One subject for every layer: the commit that is HEAD and clean. A dirty
	// tree has no subject, so a lint pass over it cannot be cited as a tag's.
	subject := CommitSubject(ctx, dir)

	lint := LintDir(ctx, dir)
	lint.Subject, lint.Verifier = subject, "local:go-vet"
	git := GitCleanDir(ctx, dir)
	git.Subject, git.Verifier = subject, "local:git-status"

	return Rotation{
		SeedID:     seedID,
		Tick:       tick,
		Invariants: []Invariant{lint, git},
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

// CommitSubject names the commit a verdict can be bound to: HEAD, if the
// working tree is clean. A dirty tree returns "" — the verdict is about bytes
// no tag names, so it is unbound rather than attributable.
//
// This is also the seam a CI-gated rotation plugs into: a tag resolves to the
// same commit, so a gated build and a local run over it share a subject and
// their marks are interchangeable by design.
var CommitSubject = func(ctx context.Context, dir string) string {
	out, err := run(ctx, dir, "git", "status", "--porcelain")
	if err != nil || strings.TrimSpace(string(out)) != "" {
		return ""
	}
	head, headErr := run(ctx, dir, "git", "rev-parse", "HEAD")
	if headErr != nil {
		return "" // no commits yet: nothing a tag could name
	}
	return strings.TrimSpace(string(head))
}

// shortSubject keeps a commit id readable in a one-line log.
func shortSubject(subject string) string {
	if len(subject) > 12 {
		return subject[:12]
	}
	return subject
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
