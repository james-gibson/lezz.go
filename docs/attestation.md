# Attestation, placement, and the per-cluster plane

**Status:** design. A proposal for using `seed.json` as an attestation of existence
in a cluster, and for what a cluster may write back into the project.

**Audience:** anyone implementing join/placement, `lezz see`, or the cluster's write
path. Read [seed.md](seed.md) first: this document extends its schema, and does not
replace the identity/placement separation it argues for.

## Summary

Three objects with three lifetimes. Mixing them is the trap.

| object | what it says | lifetime |
| --- | --- | --- |
| **attestation** | this public key was placed on this surface, by this cluster, at this time | immutable |
| **plane[cluster]** | the project's 42i position *in that cluster*, derived from that cluster's logs | recomputed |
| **cursor[cluster]** | how much of that cluster's logs have been applied here | mutable, machine-local |

`seed.json` holds the first. It must not hold the second, and it must not hold the
third.

## Why the attestation is content-free

**The cluster may modify the project.** Any receipt that hashed the tree would be
invalidated by the first write, which would make the attestation expire exactly when
the project is being used.

So bind the attestation to a **key**, not to bytes:

```
attestation = sign_cluster( cluster, project_pubkey, surface, anchor, joined_at )
```

- `anchor` is `42 + 0i` — the only number in it. It is the declared starting point,
  and the negotiation recomputes it rather than trusting the file (`seed.md`,
  §*The 42i start*).
- The private key lives outside the file, in the keychain; `seed.json` carries a
  reference (`auth.ref`), never a value. That is the existing rule, applied to the
  identity itself.
- Possession of the key is what makes the directory "the same project" after any
  amount of mutation. Nothing else in the file can do that job.

A consequence worth stating: the attestation says *existence*, not *capability*. The
rung is assigned by the cluster's verification and can change without touching the
attestation. An attestation read back as a current rung is a stale credential.

## Why there is one plane per cluster

Placement multiplies; the seed does not.

`42 + 0i` is the same baseline in every cluster — anyone can compute it. The
trajectory away from it is not shared, because the measure is taken **against the
cluster's own clock** (`lab-safety/lab/PLANE.md`, §*Axes* and §*Partitions*: a
partitioned cluster that retained internal accuracy remains accurate on
reconnection). A project in three clusters has three readings, not one sum.

The distance is a formula over observable inputs, **not stored state**
(`PLANE.md`, §*The computation*). Attributing one number to a project across
clusters would require a shared clock and a single formula; neither exists. Do not
add distances across clusters.

## The two logs

Per cluster, per project, the plane is derived from two append-only streams:

| stream | carries | written by |
| --- | --- | --- |
| **traffic log** | delivered vs promised, on that cluster's tick | the proxy |
| **isotope records** | reports and test results, per node | the alarm / consensus |

They are different streams with different writers, so one cursor is not enough: each
needs its own resume point.

Rules the log must satisfy, or it inherits figures already known to be false:

- **Dedupe by node identity.** `MATH.md` §*Worked example*: `isotope/devmode.go`
  accumulates reports without reset and a five-node cluster then reports
  `10/5 alarms agree … F=3` — `F` computed over a duplicated multiset. The tests pass
  because they assert distances, not counts. An entry without an observer identity
  reproduces the bug.
- **Record inputs, not outputs.** `(observer, nominal, real, tick)` — not distances,
  and not rungs. A rung is the bucket a derived value falls into, and the schemes do
  not reconcile: rung 2's ceiling is `80` while rung 3's is `40`, the engine maps
  `distance 80 → rung 3`, and `plane` charges of `±1` are symmetry labels rather than
  magnitudes (`MATH.md`, §*Overlapping schemes*).
- **Version the formula.** Because the value is *computed*, a formula change silently
  rewrites every past verdict. A `formula: <version>` in the log head is what keeps a
  re-derivation honest — and `MATH.md` says reconciling the schemes is open work, so
  expect that version to move.

## What is public

The accounting is public because the **anchor and the formula** are public: anyone can
compute `42 + 0i` and re-run the projection. The **inputs** are not — a traffic log is
who-called-what.

So publish the derived position plus a hash of the log head. A verifier can then check
that history was not rewritten without reading the traffic. That is the same split as
`SPLASH.md` §*Two invariants*: the traffic log and the isotope records are the audit
surface, not the advertisement.

Publishing the position does disclose cadence — a `seq` stream says how active a
project is, and when. If that matters on a given surface, publish the attestation and
the head only.

## Known limits

- **Placeholder constants.** `isotope/plane` computes by closed-form arithmetic on
  indices; the base is fixed at `42` and nothing is fitted. The weight table in
  `fortytwointegration.go` is hand-chosen. Deriving a scheme from these does not
  converge (`MATH.md`, §*Advisory*).
- **Three overlapping rung schemes**, unreconciled; a rung means what the file that
  produced it says, and must not be carried into another file's formula.
- **A cursor is not a receipt.** Applying cluster *c*'s log attests nothing; only the
  attestation does.
- **Two writers, one tree.** If two clusters write into the same project, they claim
  the same paths. Give each a disjoint mount (`.lezz/clusters/<cluster>/`) or admit
  one writer at a time; a cursor cannot disambiguate two writers.
- **Rotating a key needs both signatures.** An attestation that supersedes a key must
  be signed by the old and the new key, or existence becomes forgeable by whoever
  holds the key later.

## What `seed.json` v2 would look like

The declaration stays small, committed, and stable:

```json
{
  "version": 2,
  "project": { "name": "lezz.go", "key": "<public key>", "repo": "james-gibson/lezz.go" },
  "seed":    { "start": { "real": 42, "imaginary": 0, "rung": 0 } },
  "auth":    { "mode": "external", "ref": "keychain:LEZZ_GITHUB_TOKEN" }
}
```

Attestations and cursors do not live in it: attestations are issued by clusters and
kept as receipts, cursors are machine-local state. Putting either in a committed file
means every machine that joins a different cluster rewrites the same file, and the
merge conflict lands on the one file `lezz see` has to read.

Migration from v1: `seed.id` becomes the local routing key (`project.key`) until a
keypair exists, so `adhd.project.<id>.call` keeps working unchanged. It is a namespace
for routing, not an identity, and it is never used as evidence.

## Bootstrap identity

`lezz auth github` already provides an external login (device flow, stored as
`keychain:LEZZ_GITHUB_TOKEN`). That is enough to start: the token proves the subject at
claim time, and the attestation records that a binding exists.

- Use `github:<numeric id>` as the subject — not the login, which is mutable and
  re-issuable.
- `seed.json` keeps the reference, never the token (`seed.md`, §*Never touch the GitHub
  identity*).
- A secret gist is **not** private: it is unlisted, and readable by anyone with the
  URL. It is usable for public material (the project's public key, an attestation
  list); it is not a place for a private key or a token. If user-controlled storage is
  needed later, it is an encrypted blob or a private repository.

## Open decisions

1. **Does existence survive absence?** A register (historical: the project existed
   here at seq *n*) or a ledger (current: revocation is an entry too). This decides
   whether the log is append-only in the strict sense.
2. **How much of the sequence is public** — the derived position only, or the position
   plus the head chain.
3. **One keypair per project, or per cluster?** One is simpler; per-cluster isolates
   compromise and multiplies rotation.

## See also

- [seed.md](seed.md) — the `seed.json` schema, and identity vs placement.
- [surfaces.md](surfaces.md) — what a surface is, and what joining one means.
- `lab-safety/lab/MATH.md` — which figures are grounded, and which are placeholders.
- `lab-safety/lab/PLANE.md` — the measure as a plane, and why it is computed.
- `lab-safety/lab/SPLASH.md` — the trust model, probes, and the two invariants.
