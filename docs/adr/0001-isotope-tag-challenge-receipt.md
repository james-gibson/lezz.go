# ADR 0001 — the isotope is a tag, the challenge is freshness, the receipt is the attestation

**Status:** proposed, 2026-10-21
**Amends:** `docs/attestation.md` §*Recertification is a fresh isotope* (lines 879, 883,
890-893, 894-895), §*Isotopes are feature-driven* (648), §*Known limits* (1008)
**Depends on:** `isotope/docs/adr/0001` (identity is wording), `0004` (key schedule)

## Context

`docs/attestation.md` gives one word three jobs. Line 879 calls the isotope *"Gherkin
canonicalised and signed … the feature set and the refusals"*; line 883 says the same
token is stable; line 890 says a behaviour change implies a token change; line 894 says
a recertification *"cannot be forged"*.

The canon can carry one of those, and it is not the last. Measured against
`isotope/canonicalization.go` and its Python port:

- **"signed" is keyed, but the key is a declared input, not a secret guarantee**
  (`GenerateIsotope:273`). `brine_audit.py:29` states the consequence: *"detects
  corruption, not forgery."*
- **"a behaviour change implies a token change" is false in both directions.** A table
  row (`base` → `admin`) and a `Background:` anchor (`0i + 42` → `99i + 99`) leave the
  token identical (`eOBhPmeUtD6x`, `901drSV8wbVz`); a tidy reorder changes it
  (`JD1AiOZHzDaq` → `x3gJwfrTjkZK`).
- **"stable" is true**, and is the property that creates the replay hazard the doc
  already handles correctly.

So the defect is vocabulary, not mechanism. The document is right that the token must
never re-enable anything; it is wrong to say the token is what proves the re-enabling.

## Decision

**Three objects, three lifetimes, one name each.**

| object | answers | entropy | lives |
| --- | --- | --- | --- |
| **isotope** | *what* is promised | none that is secret — a tag; a keyed checksum, not a signature | stable, as long as the spec |
| **challenge** | *when*, and to whom | fresh, bound into the run | one run |
| **receipt** | *who* accepted it, at which tick | the certifier's key | one attestation |

Amendments to the doc, sentence by sentence:

1. **Line 879** — replace "canonicalised and signed" with "canonicalised and keyed by a
   declared class key"; and "the feature set and the refusals" with "the *wording* of
   the declared feature set" (ADR `isotope/0001`).
2. **Line 883** — keep. Stability is real and the replay reasoning built on it stands.
3. **Lines 890-893** — replace with: *"if the fix changed what the node does **on a step
   line**, the wording differs."* Add the counterexamples above, because a reader who
   believes the current sentence will certify a table-driven fix that changed nothing
   legible.
4. **Lines 894-895** — delete "cannot be forged". Forgery is prevented by the quorum over the
   run result, not by the token. What re-derivation buys is *consistency*: the token
   names a spec, and anyone holding the spec can check the name. It cannot tell who
   wrote it, because the key is either public (dev) or held by a class (prod).
5. **Line 648** — the community tag should be the **per-scenario part ID**, not the
   whole-file isotope. Communities are attachable per feature; a whole-file token
   changes identity when any scenario is added, so it cannot be a community.
6. **Line 1008** — keep, and strengthen the derivation: *"declared" and "demonstrated"
   are the same 43 characters because the key is public or class-held.* The transcript
   is the only separator, which is why the challenge exists.

**And for change legibility, cite brine's vocabulary, not the raw token.** `brine.py`
already distinguishes the four outcomes (`classify()`, `:861-874`) from a digest pair:

```
new · unchanged · reordered · changed
same set_digest, different order_digest  → a pure reorder
different set_digest                     → the parts themselves changed
```

That is what makes "what changed since the fire" answerable. The whole-file isotope
alone answers "something changed" and cannot say whether it was behaviour.

## Consequences

| claim in the doc | after |
| --- | --- |
| the isotope is signed | the isotope is a keyed class tag; the receipt is signed |
| the token proves the moment | the transcript proves the moment |
| a behaviour change shows in the token | a *step-line* change shows in the token; a table or `Background:` change does not |
| the recertificate is unforgeable | the recertificate is unforgeable *because a quorum signed the run* |
| isotopes are communities | per-scenario part IDs are communities |

Nothing in the sequence `declare → canonicalise → sign → challenge → run → result →
quorum → attest` changes. What changes is which step the word "isotope" is allowed to
describe.

## Known limits

- **The word still has a foot in both camps.** "The isotope is feature-driven" remains
  true of a part ID; a reader can still hear "unforgeable" in "signed". The doc's own
  warning about `seed` carrying two meanings (issue #21, addendum 2) applies unchanged
  here.
- **Class-key forgeability is invisible to a peer inside the class.** A member can mint a
  token for a spec it never ran. Only the challenge transcript separates declared from
  demonstrated, and only a party outside the class can check independence.
- **The counterexamples are the short list, not the set.** Any construct the canon drops
  (ADR `isotope/0001`) is another one; the list should be generated from
  `validate_spec.py`'s warnings rather than maintained by hand.
- **Rewriting lines 879/890-893/894-895 touches a merged doc.** It should land as its own commit
  with the vectors, so the correction is auditable against the claim it replaces.

## What would reverse this

Making the token the evidence — i.e. a per-node signature over the spec that gates
admission on its own. That requires a fresh session key (ADR `isotope/0004`), which
destroys stability, which means the object being signed is a *run* and not a *spec* —
at which point it is the receipt under a different name, and the doc's three-object
table should collapse to two.
