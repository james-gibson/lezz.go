# `le.roy` — rotate or yield

**Status:** proposal. Not implemented. Depends on `internal/seed` as it stands today
(`Rotation`, `Invariant`, `Applies`, `Yield`).

`le.roy` is a one-shot lab tool with a single accepted request: a rotation challenge. It
answers in one of exactly two ways — **rotate** (advance the claim) or **yield** (hand it to
another claimant) — and refuses anything else by name. The name is the pun it sounds like: *le
roi*, the one holding the rotation, who may either turn it or give it away.

## Why a tool and not a convention

The rotation model already says what a claim is: a subject (a commit), a claimant, a set of
invariants, each with a verifier and a mark. What it does not yet say is who may *act* on one.
A convention would say "agents should yield politely"; a tool can make the alternative
impossible to express.

So `le.roy` accepts the **challenge** and never the **answer**. A rotation document that
arrives with `pass: true` in it is a claim about a verdict, not a verdict: the tool checks the
subject, re-runs the verifiers, and decides. That is the whole security property — an agent can
lie in a file, and the lie does not survive the tool.

## The accepted challenge

```
le.roy check  [--dir PATH] [--claimant NAME] [--tick N]
le.roy rotate [--dir PATH] [--claimant NAME]
le.roy yield  --to NAME [--dir PATH] [--claimant NAME]
```

- `--dir` defaults to `.` and names the workspace; nothing outside it is read.
- `--claimant` defaults to `$LE_ROY_CLAIMANT`, then to the current unix user. An empty claimant
  is refused: a passing claim with no claimant is indistinguishable from another workspace's.
- `--tick` is the caller's counter. The tool does not invent one; it echoes it, incremented, so
  the caller owns the schedule and the tool owns the verdict.

Everything else — `start`, `stop`, `edit`, `assert`, `sign`, `force`, `--skip`, `--trust`,
`--yes` — is refused with one fixed message:

```
le.roy accepts the rotation challenge only
```

That message is the tool. If a request can be expressed, the tool has failed.

## What it does with the challenge

Both verbs build the rotation from the workspace, not from the request:

1. **Bind the subject.** `git rev-parse HEAD`, but only when `git status --porcelain` is empty.
   A dirty tree has no subject, so nothing can be bound to it.
2. **Verify every invariant** in the workspace — today `go vet` and cleanliness, tomorrow
   whatever the verifier list names. Each verdict records its verifier (`local:go-vet`,
   `local:git-status`, later `ci:<workflow>@<run>`).
3. **Decide.**

| verb | accepted when | refused when |
|---|---|---|
| `check` | always (read-only) | the workspace is not a directory |
| `rotate` | every invariant holds, the subject is bound, and the claimant is named | any inverted layer, an unbound subject, or no claimant |
| `yield` | `Rotation.Yield(to, HEAD)` succeeds | no recipient, the same claimant, an inverted rotation, or a subject that moved |

A refused rotation is printed **inverted** with its reasons and exits `2`. The tool never
rotates a broken state into a good-looking one: an inverted rotation is a repair to report, not
work to advance.

## Output

One JSON object on stdout, one line, so it composes with a pipe and with Gherkin assertions:

```json
{
  "tool": "le.roy",
  "verb": "yield",
  "exit": 0,
  "rotation": {
    "seed_id": "20261027T004751-e9c4252d",
    "tick": 41,
    "claimant": "agent-b",
    "subject": "9230d12…",
    "invariants": [
      {"name": "lint", "pass": true, "verifier": "local:go-vet", "subject": "9230d12…"},
      {"name": "git",  "pass": true, "verifier": "local:git-status", "subject": "9230d12…"}
    ]
  },
  "refusals": []
}
```

Exit codes: `0` accepted, `2` refused (the challenge was understood and declined), `1` usage or
I/O. Nothing is written to the workspace, no state file is created, and there is no history: a
rotation history belongs to whoever is keeping the ledger.

## Non-goals

- **Not a scheduler.** It does not decide who goes next; it decides whether this claim is
  citable. Order is the caller's business.
- **Not a git client.** It reads `HEAD` and `status`; it never commits, tags, pushes, checks
  out, or resolves anything.
- **Not a linter.** It runs the verifiers and records their verdicts; the quality bar is
  whatever they enforce.
- **Not a trust authority.** A rotation still names its verifier, so a receiver that trusts
  only gated runs must refuse a locally prepared rotation even though it applies
  (`docs/sea-hub.md`, D9–D13 for the surrounding shapes).
- **No network, no daemon, no root.** One process, one request, one answer.

## What it proves, and what it does not

It proves that *this workspace, at this commit, passed these verifiers, claimed by this
claimant* — which is enough for a handover and for putting work in an order.

It does not prove the verifiers were adequate, that the claimant is who it says, or that the
work is good. Those belong to the verifier class and to the receiver's policy. The tool's
contribution is narrower and more useful: **it makes a claim checkable, and a handover
explicit.**

## Acceptance sketch

```gherkin
Scenario: le.roy rotates a clean workspace
  Given a clean workspace at a commit
  When I run "le.roy rotate --claimant agent-a"
  Then it exits 0
  And the rotation names the same subject, the next tick and agent-a

Scenario: le.roy refuses to rotate a broken workspace
  Given a workspace with a lint failure
  When I run "le.roy rotate --claimant agent-a"
  Then it exits 2
  And the rotation is inverted and names lint
  And no rotation is printed as accepted

Scenario: le.roy yields the evidence, not a summary
  Given a clean workspace a rotation was taken over
  When I run "le.roy yield --to agent-b"
  Then it exits 0
  And the claimant is agent-b
  And every verifier is unchanged

Scenario: le.roy accepts no other challenge
  When I run "le.roy assert --correct"
  Then it exits 2
  And it prints "le.roy accepts the rotation challenge only"

Scenario: an asserted pass is not a verdict
  Given a rotation document that claims "pass": true with no verifier
  When I run "le.roy check"
  Then the assertion is discarded
  And the verdict is recomputed from the workspace
```

## Where it lives

`cmd/le-roy` in this repository, over `internal/seed`. It ships like the other lab tools —
a signed release binary, no install path assumptions — and it is deliberately *not* wired into
`lezz sea`: the hub supervises clusters, `le.roy` supervises handovers, and coupling them would
make each one harder to reason about.
