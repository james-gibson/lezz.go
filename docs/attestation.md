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
- `joined_at` is absolute UTC, not a tick slot. A receipt stamped with `mod(UTC, 5s)`
  would recur every five seconds and could be presented as a fresh join — see *The
  tick* below.
- Possession of the key is what makes the directory "the same project" after any
  amount of mutation. Nothing else in the file can do that job.

A consequence worth stating: the attestation says *existence*, not *capability*. The
rung is assigned by the cluster's verification and can change without touching the
attestation. An attestation read back as a current rung is a stale credential.

## Why there is one plane per cluster

Placement multiplies; the seed does not.

`42 + 0i` is the same baseline in every cluster — anyone can compute it. The
trajectory away from it is not shared: the **tick is shared by default** (below), but
the measurement chain is not. `PLANE.md` (§*Axes*, §*Partitions*) makes the accuracy
claim against the cluster's own clock, so a partitioned cluster that retained internal
accuracy remains accurate on reconnection. A project in three clusters has three
readings, not one sum.

The distance is a formula over observable inputs, **not stored state**
(`PLANE.md`, §*The computation*). Two clusters on the same tick can be *compared* at
the same slot; they cannot be *added*, because that would assume one formula and one
observer chain. Comparison and addition are different operations.

## The tick: a shared phase, not a shared counter

The linear axis has a default source: `mod(UTC, 5s)`. It is public and computable by
anyone, so two clusters that share nothing — no federation, no registry, no key
exchange — still agree on when the current slot is. That is what lets unrelated
clusters speak to each other: they rendezvous on the same five-second slot without
first agreeing on a counter or an authority.

Two things it does and does not buy:

- **Alignment, yes.** A position sampled in cluster A at slot *k* and one sampled in
  cluster B at slot *k* are readings of the same phase, and may be compared.
- **Order, no.** `mod` discards the cycle, so slot *k* recurs every five seconds. An
  entry carrying only a slot can be replayed as "this tick" indefinitely — the stale
  credential's failure mode, in time. Freshness needs the absolute timestamp (or a
  cycle count) beside the slot, and the log carries both: absolute time orders, the
  slot compares.

Resolution follows from the wheel: two events inside one five-second slot are the same
tick and cannot be ordered by it. An entry that needs sub-slot ordering carries the
absolute time; the tick is the coordinate, not the clock.

A cluster may run its own tick instead — the federation's heartbeat and announce
intervals are the internal variant (`PLANE.md`, §*Axes*). A plane on a private tick is
comparable only to itself: that is the partition case, and it remains accurate while
partitioned.

## Negotiation is a handshake in three ticks

Three ingredients: the tick wheel (a shared phase), the 42i position (the measure),
and three tick cycles (the flights).

| flight | slot | carries | TLS analogue |
| --- | --- | --- | --- |
| 1 | *k* | `42 + 0i`, the surface requested, the project key | ClientHello / ServerHello — public parameters, no secret |
| 2 | *k+1* | the cluster's attestation: `(key, cluster, surface, anchor)` | Certificate — binds a key to a domain, and names the domain reached |
| 3 | *k+2* | proof of possession of the project key | CertificateVerify / Finished |

**Why three slots, rather than as fast as possible.** The wheel gives alignment but no
order inside a slot (*The tick*), so one flight per slot is what makes the sequence
ordered at all: three flights, three distinguishable slots, no timestamp needed to
order them. The cost is structural — a handshake takes at least three slots, fifteen
seconds at the default — and a peer cannot distinguish "slow" from "absent" until its
slot passes. A retry after a partition therefore costs slots, not milliseconds.

**What the attestation does in that shape: it orients the domain.** Before anything
dependent on placement is sent, the peer learns which cluster and which surface it is
actually talking to. That is the certificate-name check — not the session key. The
attestation is public, it authorises nothing, and it does not move when the session's
trust does.

**The asymmetry with TLS, stated so it is not over-read.** `42 + 0i` is a coordinate
origin, not a trust anchor: anyone can compute it, which is why the existing docs call
the 42 handshake a position check rather than authentication. TLS terminates trust in a
CA; here it terminates in the cluster's verification — registry, mDNS, rung. **The
handshake orients; it does not admit.** Admission is a separate step, and it is the one
that assigns capability.

## Stabilise, then cluster: the handshake is not a gate

Three flights in three slots is fifteen seconds on the default wheel. In front of the
connection that is a stall longer than most ACP/MCP clients will wait for one, so
placement cannot be a precondition of connecting.

Placement is a later step:

```
connected ──► stabilising ──► handshaking ──► placed
                   │                │
                   │                └────────► unplaced (policy) ──► refused
                   └─ local verbs usable
```

1. **Connect.** The transport comes up immediately and the session exists. Local verbs
   work. Nothing about placement is claimed.
2. **Stabilise.** Observe *K* slots and confirm the peer's slot boundaries agree with
   ours — a rendezvous on `mod(UTC, 5s)` only holds while skew is well inside the
   wheel. This is a clock observation, not a trust claim, and it is the part worth an
   explicit option (`--stabilise K`, small default), because a peer whose skew is
   unstable will mis-align on every flight after it.
3. **Cluster.** Run the three flights, one per slot, as specified. Placement is known
   after flight 2 and final after flight 3.

**The invariant the compromise must not break: unplaced is not a surface.** A
connection that has not completed flight 2 is not on `base`, not on any configured
surface, and must not be defaulted to one — `seed.md` is explicit that a folder with
no key must never auto-create a trusted surface. While unplaced a caller may use local
verbs and may ask to wait; a cluster call waits for placement or is refused. It is
never served at an assumed rung.

**Placement is recorded per log entry.** Because a call can now precede flight 2, the
traffic log carries the placement in force (`placed: false`, or the placement sequence
number) on each entry. Otherwise an entry written before placement is re-attributed to
it once the handshake completes, and the plane shows a departure measured under a
placement that did not yet exist.

What it buys: connect latency is transport latency rather than three slots; placement
latency is still three slots after stabilisation; and a caller that genuinely needs
placement up front asks for it (`--wait`, or awaiting `placed`) instead of every caller
paying the fifteen seconds.

## Who pays the fifteen seconds

The three flights are the price of having **no identity to present**. They are not the
price of connecting.

| path | who | latency | what it may do |
| --- | --- | --- | --- |
| **pre-baked** | a client that already holds an isotope: an attestation receipt from a previous placement | one message — launch with the receipt | whatever the previous placement allowed, until revalidation says otherwise |
| **cold** | a client with no identity | stabilise *K* + 3 slots | local verbs meanwhile; placed after flight 2 |
| **visitor** | an agent that wants project-specific answers and no cluster capability | none | answers from the declaration and cached material |
| **proxied** | a visitor that does need cluster capability | the proxy's | answered by a placed peer, on the proxy's placement |

### Pre-baked: orientation without round trips

This is what the attestation buys. It names the domain — cluster, surface, anchor — so
a client holding one does not have to discover it. It presents the receipt and is
oriented before it speaks: resumption, not a fresh handshake.

But a pre-baked isotope is **a stale credential by construction**, and the lab's own
measure says why that matters — *a stale credential and a clock skew are the same
reading* (`PLANE.md`, §*Plane, not line*). So the receipt is admitted with its **age
declared**: the slot it was issued, and slots elapsed. Revalidation against the
cluster's head runs concurrently, and revocation can end the session.

Age is an input to the plane, not a hidden property of the connection. Presenting a
three-day-old receipt as current *is* a departure, and it should be counted as one
rather than silently allowed. Entries in that window record `placement:
resumed(receipt_seq, age_slots)` in the log, so a later revocation does not
retroactively rewrite what was measured.

### Visitors: project answers, no cluster

An agent that just visits a project wants answers about that project — what it is, what
it declares, where it sits — and does not need a cluster connection to get them. That
path is served from the declaration (`seed.json`) and cached material (the last known
plane position, the docs), with no handshake and no capability claim.

When a visitor does need cluster capability, it is **proxied into a cached version**: a
placed peer answers on its behalf, and the traffic log records `on_behalf_of`. Without
that, the visitor's departures are attributed to the proxy's placement and the
accounting is false — the proxy's rung would move for somebody else's calls.

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

The accounting is public because the **anchor, the formula, and the tick** are public:
anyone can compute `42 + 0i`, re-run the projection, and read the current five-second
slot. The **inputs** are not — a traffic log is who-called-what.

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
- **A slot is not a timestamp.** `mod(UTC, 5s)` recurs, so anything stamped with the
  slot alone can be presented as current. Order on the absolute time, compare on the
  slot, and never let a slot decide freshness.
- **A whole handshake can be replayed, not just a flight.** Three flights in three
  slots are still three recorded messages; without a per-handshake nonce in flight 1
  and an absolute `joined_at` to check, a recorded exchange can be presented verbatim
  later. The same records that make the handshake ordered make it replayable.
- **Unplaced is not untrusted, and the errors must not be merged.** "Not placed yet"
  is a wait state; "refused" is a verdict. Mapping both onto one error teaches callers
  to retry a trust failure as if it were latency.
- **Resumption skips round trips, not checks.** A pre-baked receipt is oriented before
  it speaks, but the domain it claims still has to be revalidated against the cluster's
  head, and the session has to remain revocable. A resumed session that cannot be
  revoked is a standing grant, which is what the attestation was designed not to be.
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
4. **Stabilisation as slots or as a skew bound?** `--stabilise K` counts slots, which
   is easy to reason about but assumes the peer's clock drifts smoothly. A bound on
   observed skew (milliseconds) is the honest quantity, and much harder to specify
   against a five-second wheel.
5. **How long is a pre-baked receipt valid, and is age capped or scored?** A hard
   validity window is simple and refuses a returning client after it lapses; treating
   age as distance means a stale receipt still works but starts the session already
   departed. The second is more consistent with the measure and harder to explain.

## See also

- [seed.md](seed.md) — the `seed.json` schema, and identity vs placement.
- [surfaces.md](surfaces.md) — what a surface is, and what joining one means.
- `lab-safety/lab/MATH.md` — which figures are grounded, and which are placeholders.
- `lab-safety/lab/PLANE.md` — the measure as a plane, and why it is computed.
- `lab-safety/lab/SPLASH.md` — the trust model, probes, and the two invariants.
