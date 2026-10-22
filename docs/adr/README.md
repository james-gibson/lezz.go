# Architecture decision records — lezz

Each ADR is one decision, the evidence for it, and what it costs. They are numbered and
never renumbered; a superseded ADR stays in place and points at its replacement.

| # | decision | status |
| --- | --- | --- |
| [0001](0001-isotope-tag-challenge-receipt.md) | the isotope is a tag, the challenge is freshness, the receipt is the attestation | proposed |
| [0002](0002-preconditions-are-given-steps.md) | placement preconditions are `Given` steps, never a `Background:` | proposed |
| [0003](0003-seed-is-the-ground-truth.md) | `seed` names the ground truth; the routing id is `project.route` | proposed |

**Status vocabulary:** *proposed* (written, not ratified) · *accepted* · *rejected* ·
*superseded by NNNN*.

All three were originated on 2026-10-21 from the review recorded in
[james-gibson/lezz.go#21](https://github.com/james-gibson/lezz.go/issues/21). They do not
replace `docs/attestation.md` or `docs/seed.md`; they correct four sentences in the first
and rename one field in the second. The canon they are written against is `isotope`'s,
documented in `isotope/docs/adr/0001`–`0004`.

Where an ADR says "measured", the vector is in the ADR so it can be re-derived with
`kb/projects/scripts/brine.py` rather than trusted.
