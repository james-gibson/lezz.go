# ADR 0003 — `seed` names the ground truth; the routing id is `project.route`

**Status:** proposed, 2026-10-21
**Amends:** `internal/seed/seed.go:31-34` (`Seed.ID`), `docs/seed.md` §*Schema*
**Source:** [lezz.go#21](https://github.com/james-gibson/lezz.go/issues/21), addenda 2 and 3
**Rejects:** the "make the id a proper isotope" and "link the id to a universe" proposals in
the same issue

## Context

`Seed.ID` (`internal/seed/seed.go:32`) is documented as *"stable project identity"* and
is minted by `newID()` (`:184`) as `time.Now().UTC().Format("20060102T150405")` plus four
random bytes. Three different kinds of thing sit in the `seed` block under one word:

| negotiation role | field | what it actually is |
| --- | --- | --- |
| project | `project.name` | label |
| where | `seed.surface` | placement |
| a seed | `seed.start` (`42 + 0i`, rung 0) | the ground truth to measure from |
| *(not in the tuple)* | `seed.id` | local namespace / routing key |

So one of the four is a seed. The id is the one that is not, and calling it a seed is
what made it look like it should carry identity — which is how issue #21 started.

The label also decides the question rather than answering it: a routing key is not
asked to be a universe id or an isotope, because it is never used as evidence. And the
mechanical constraint agrees — `idPattern` is `^[A-Za-z0-9_.-]+$` (`:60`), so an isotope
token (`iso-login-v1:a3f2…`) cannot validate as an id even if it were wanted.

## Decision

**The id leaves `seed` and becomes a property of the project. `seed` is reserved for
`start`.**

```json
"project": { "name": "grep", "route": "20261019T210927-09001816" },
"seed":    { "surface": "base", "start": { "real": 42, "imaginary": 0, "rung": 0 } }
```

- `Project.Route` is the field name — not `Project.Key`. The protocol already uses *key*
  for signing keys and class keys (`isotope/docs/adr/0004`); naming a namespace a key in
  the same document set reproduces the exact conflation this ADR is fixing, one word
  later.
- The value is unchanged, arbitrary, and validated by `idPattern`. It is not an isotope,
  not a universe id, and not derived from anything.
- `adhd.project.<route>.call` keeps its shape; only the source of the `<route>` segment
  is renamed, so no dynamic-tool name changes.
- `seed.id` is read as a legacy alias for one release, then removed. `lezz seed init`
  continues to mint `project.route` exactly as it mints `seed.id` today.
- `lezz see` prints the route and the start as separate, separately labelled facts, so a
  reader cannot mistake the timestamped string for a position.

## Rejected alternatives

| alternative | why rejected |
| --- | --- |
| `seed.universe: "personal:lezz_go:2026"` | mints a place in the graph for something that is not in the graph; also risks declaring a dangling universe (`kb/lore/OUTSTANDING.md` records that failure) |
| upgrade `id` to an isotope (`iso-…`) | `idPattern` rejects `:`; and an identity used as evidence is exactly what makes a token forgeable-looking — see `isotope/docs/adr/0004` |
| keep the name `seed.id`, document the double meaning | documentation does not survive; the word is read before the note is |
| `project.key` | the crypto sense is already taken in this protocol; see above |

## Consequences

| reader | before | after |
| --- | --- | --- |
| `lezz see` | prints "seed id" | prints "route" and "start" |
| adhd dynamic tool | `adhd.project.<id>.call` | unchanged shape, new field |
| schema validator | `seed.id` required | `project.route` required, `seed.id` accepted as legacy |
| a reader of `seed.json` | one word, three meanings | `start` is the seed; `surface` is placement; `route` is routing |

## Known limits

- **`surface` is still in `seed`, and it is placement, not a seed.** Moving it is a
  separate decision with its own migration; leaving it here means the block is now
  "placement + seed" rather than three things, which is better but not clean.
- **The route is unmemorable and unresolvable by design.** Anyone expecting `lezz
  project find <route>` will not find one; it names a directory's binding, not a thing
  in a graph.
- **A legacy `seed.id` and a new `project.route` can both be present and disagree.** The
  reader must prefer `project.route` and warn, not merge.
- **Renaming a field changes every committed `seed.json`.** There is no schema version
  bump proposed here; the `version` field (`:23`) exists and should be incremented in the
  same change, or old readers will silently ignore the new field.

## What would reverse this

A decision that a project *does* have a graph identity — at which point the correct move
is a new field holding an enrolled universe's id with a verification that it resolves,
not a promotion of the routing id. That is issue #21's original proposal, and it remains
rejected only on the evidence that the routing key is never used as evidence.
