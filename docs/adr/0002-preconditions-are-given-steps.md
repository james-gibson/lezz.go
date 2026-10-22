# ADR 0002 — placement preconditions are `Given` steps, never a `Background:`

**Status:** proposed, 2026-10-21
**Amends:** conformance specs under `features/lezz/`; `docs/attestation.md` §*Stabilise,
then cluster*
**Depends on:** `isotope/docs/adr/0001` (identity is step text)

## Context

The canon drops a `Background:` entirely: `ParseGherkin` accepts steps only while a
scenario is open (`canonicalization.go:119`), and a background precedes the first
`Scenario:`, so its steps are never seen. Measured on the live spec
`features/lezz/settings-wiring.feature`:

```
isotope with its Background       bQVc9DwbA6bpoXoZHISAtuqwtqNHbXB5BZK6ZQYQWI8
isotope with the block deleted    bQVc9DwbA6bpoXoZHISAtuqwtqNHbXB5BZK6ZQYQWI8
```

Identical — 75 tokens either way. The two preconditions that file states in its
`Background:` (*"Zed's settings file contains an agent `oc`"*, *"the file contains a
comment and keys unrelated to agent servers"*) are load-bearing for its first two
scenarios and are not in its promise at all.

This matters to the attestation specifically, because this repo's own protocol states
its preconditions in exactly that shape. `docs/attestation.md` §*Stabilise, then
cluster* makes `surface`, the `42i + 0` anchor, the rung, `--stabilise K` and the
receipt's issue slot preconditions of a placed run — and the attestation binds
`(key, cluster, surface, anchor)`. Written as a `Background:`, the `anchor` would be
bound in the attestation and absent from the isotope: a passing recertification that
never covered the placement it depended on.

## Decision

**In any spec that carries an isotope — which is every spec in `features/` — a
precondition is a `Given` step, repeated per scenario, never a `Background:`.**

The placement preconditions are therefore spelled as steps, e.g.

```gherkin
Scenario: a cold peer is placed after flight three
  Given the anchor is 0i + 42
  And the declared surface is base
  And the cluster admits rung 0
  When the peer completes the handshake
  Then it is placed at surface base
```

Consequences taken deliberately:

- **Repetition is the price.** The canon has no shared-step mechanism that survives
  hashing, so a precondition used by three scenarios is written three times. That is a
  feature: each scenario's promise then names the world it assumes.
- **The precondition is identity.** Changing `anchor` from `0i + 42` changes the isotope,
  which is the intended reading — a promise made against a different origin is a
  different promise.
- **`validate_spec.py` moves from warning to error** for a `Background:` in a spec it is
  asked to place. A warning has been sufficient while specs were read by humans; it is
  not sufficient once a token is the promise.

## Consequences

| construct | today | after |
| --- | --- | --- |
| `Background:` in a spec | warning, silently dropped | error; not placeable |
| preconditions on a step | hashed | hashed — as intended |
| anchor in the attestation | binds an unstated assumption | binds a stated one |
| spec size | one shared block | linear in scenario count |

`features/lezz/settings-wiring.feature` is the known violation and should be fixed in the
same change: its two background steps move into the two scenarios that need them.

## Known limits

- **Correctness does not follow.** A `Given` step is hashed, not executed; the token now
  promises the precondition and still says nothing about whether it held. That is the
  `Given`/`When` split the whole lab rests on, not a gap in this decision.
- **`Given` steps are covered by ADR `isotope/0001`'s blind spots.** `the anchor is
  0i + 42` and `the anchor is 0i + 43` differ (the digits survive normalization), but a
  step that states an ordering (`0 < 1`) does not distinguish its reverse. Preconditions
  must be stated as values, not as comparisons.
- **A repeated precondition can drift.** Three copies can diverge; nothing enforces that
  the same string is used. A cross-spec check for "same intent, different wording" is
  not attempted here.
- **A `Background:` is still legal Gherkin**, so a spec can pass a human review and fail
  this rule. The rule needs the validator, not vigilance.

## What would reverse this

A canon change that hashes `Background:` content as part of every scenario — which would
require the parser to keep a background block and fold it into each scenario's raw. That
is a larger change than ADR `isotope/0001`–`0003` and buys the same thing this rule buys
with repetition, so it is not proposed.
