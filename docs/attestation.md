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

## A, B, C, and the doorbell

The three flights are not three messages to one node. They are three messages to three
roles, and that separation is why the latency cannot collapse:

| flight | node | verification role | admission layer |
| --- | --- | --- | --- |
| 1 | **A** | ingress | presence — the session |
| 2 | **B** | verifies A | surface — the dynamic tool array |
| 3 | **C** | monitors B | authority — the credentials |

Each node is verified by the next, so no node reports on itself
(`lab-safety/lab/RECONCILE.md`, §*Topology* and §*Admission layers*). One round trip to
one node would put presence, surface, and authority in a single process, which is the
thing the triad exists to avoid.

**And the FIFO is a doorbell, not the queue.** The lab already has this pattern
(`docs/queue.md`): one byte per enqueue wakes the worker, the spool of `jobs/*.json`
is the order of record, and a missed doorbell is harmless because the spool is the
source of truth. The tick is the same shape at a coarser resolution:

> **The tick wakes. The log orders.** A dropped slot costs latency, never state.

That is the resolution of the earlier ordering problem. `mod(UTC, 5s)` is a doorbell —
it says *now*, not *which came first* — and the absolute timestamps in the log are the
spool. A slot may be missed; the log is what survives it.

### Prewarm

A client with a real reason to be there passes A and B ahead of time, so its first
real call finds presence and surface warm and only C's authority check remains. Cold,
that same call pays queue depth plus three slots.

**Prewarm warms presence and surface. It never warms authority.** Authority is
per-use; warming it would convert a credential check into a standing grant, which is
exactly what the attestation was designed not to be. So the saving is bounded — two of
the three slots — and trust-neutral: no rung moves, no check is skipped.

Two legitimate ways to be warm:

- **resumption** — hold an isotope receipt; the domain is oriented before the first
  message (*Who pays the fifteen seconds*), and its age is counted;
- **registration** — reserve through the FIFO with the project key before the call is
  needed. A reservation is scheduling, not trust: it buys position in the queue, not
  permission.

**The cost is conserved.** A cold client pays in slots; a warm one pays in upkeep,
because a warmed session that stops refreshing becomes a stale credential — and a
stale credential is a departure the plane will count. Nobody gets the fast path free;
somebody just chooses whether to spend it in latency or in attention.

**The chain is not Byzantine-tolerant at three nodes.** `F = ⌊(N−1)/3⌋` gives `F = 0`
at `N = 3` (`MATH.md`), so A/B/C is a *verification* chain — each node checks a
different property, and a fault falls to a neighbour — not a consensus set. Claiming
fault tolerance for the golden triangle would be claiming a figure the arithmetic does
not support; Byzantine tolerance starts at four nodes.

## The fabric: three processes, stacked, feature-driven

A, B, C are not abstractions — they are **process separate**, and that separation is
what the three slots cost:

| node | process | why that one |
| --- | --- | --- |
| **A** | `lezz.go` | hosts and manages the session: presence |
| **B** | `adhd` | mints the dynamic tool array (`adhd.project.<id>.call`): surface |
| **C** | `ocd-smoke-alarm` | monitors and holds the isotope records: authority |

**Any of them can be stacked into another.** Stacking is `SLA.md` §12's route
reflection: *a sub-cluster is a node to its parent and a cluster to its members.* One
process then carries more than one role, which is legitimate — and it is not free:

- **Stacking buys latency and spends isolation.** Flights that cross a process
  boundary cost a slot; flights inside one process need not. A stacked instance can
  answer in one slot instead of three.
- **It also spends the property the triad exists for.** *No node reports on itself*
  (`RECONCILE.md`) holds only while the roles are in different processes. A stacked
  node verifying its own ingress is a self-report, whatever the diagram says.
- So stacking is a **declared** arrangement: a demo, a dev cluster, a single-host
  install. It must not be silent, because the latency and the guarantee move together.

### A golden cluster is a single node to its neighbours

A reference ("golden") public cluster presents **one endpoint** to other clusters, and
the triad hides behind it. That is route reflection again, one level up: the cluster is
a peer to its neighbours and a cluster to its members, so a neighbour speaks to an
address rather than to three roles.

This is why **addresses persist**. The endpoint is a rendezvous, not a process: roles
swap underneath it and the address stays, and `SLA.md` §12's *graceful restart* is what
makes that honest — the peer already holds the mirrored traffic, so a restart is a role
swap rather than a cold start. The persistence is also why `ADDRESSING.md`'s rule
matters: *an address is a claim.* An address that outlives its process is exactly the
case where the claim can be stale, so it is re-verified, never inherited.

### Isotopes are feature-driven

Routing is by **feature, not by identity**. `SLA.md` §12 names the mechanism: BGP
**communities** (RFC 1997) — a tag a peer acts on by policy without knowing the
advertiser. The isotope is derived from the *declared feature combination*, which is
what makes a route meaningful to a stranger: the caller asks for a feature, not for a
node.

Two consequences follow, and both are already stated in the source:

- **An advertisement is reachability, not authority.** Presence (mDNS: *I am here*) and
  authority (isotope records: *I am authorized to this degree*) are announced
  separately — holding an isotope is not reachability.
- **A hijacked announcement is detectable**, because the isotope is canonicalised and
  re-derived rather than trusted. That is the signature BGP never had.

### The table is learned, and dampening is the measure

The routing table is populated by announcements, not configuration. Selection is
**local policy**, not shortest path; withdrawal is **self-demotion**; and a flapping
route is **suppressed, not banned** — `RFC 2439` dampening, a decaying penalty.

That is the 42i measure doing routing work: *a caller that keeps departing is removed
from the routeable set* (`kb/scripts/lab/README.md`), and it returns as the penalty
decays. Nothing is banned; the record over time does the work.

**Speed is a capacity question, not an admission shortcut.** `SLA.md` §12: *the answer
to more traffic is another door — and a peer is another door, reached by an
advertisement rather than by configuration.* An agent that needs speed adds cluster
capacity (an announcement, then a route) or prewarms (*A, B, C, and the doorbell*). It
does not get a faster path by skipping A, B, or C — there is no such path to give.

**Named cost, taken from the source.** BGP converges slowly, by path exploration
(Labovitz, Malan & Jahanian, SIGCOMM 2000), so a cluster on a tick inherits that shape:
convergence is a property *over time* rather than an instant, and a route's history
belongs in its score rather than in a footnote.

**And the filed gap is the liveness one.** `SLA.md` §12 records that hold timer /
keepalive was *not* adopted: *a name is not a heartbeat* (smoke-alarm#35). An
announcement can outlive the thing it advertises, and the tick is a doorbell, not a
proof of life. *Durable workspaces are the keepalive* (below) is the proposed closure;
until it is built, a persistent address is a claim with no expiry of its own.

## Durable workspaces are the keepalive

The liveness gap (`SLA.md` §12: *a name is not a heartbeat*, smoke-alarm#35) closes
without a hold timer, if the workspace is the record. That is what the workspaces work
was for: a session store that is a **durable filesystem** — a VFS with drivers, not a
process's memory (`lab-safety/lab/workspaces/`, `lezz.go/docs/queue-acp.md`).

A keepalive is a message that asserts liveness. A durable record doesn't assert
anything, so liveness has to be *derived*, by someone else, from whether the record
advanced:

| keepalive | durable workspace |
| --- | --- |
| periodic "I am alive" message | the record advances only if the writer acts |
| timeout → withdraw | stagnation over *K* slots → dampen: suppressed, not banned |
| self-reported | observed by the verifier from the artifact (`RECONCILE.md`: B holds A's records) |
| restart loses state | restart resumes the store — `graceful restart`, for free |

That is the useful inversion: **nobody has to be asked whether they are alive.** The
tick says *when to look*; the store says *whether anything happened*. A node that has
stopped is not marked down by its own admission — it is missing from its own record,
and the neighbour that holds the mirror can see it.

Four things this does not do, and they matter:

- **A durable file is not a live participant.** A crashed process leaves its store
  intact. Existence is not freshness; only advancement is, and only a third party
  should judge it. A frozen writer with a healthy store looks alive to anyone checking
  for the file rather than for movement.
- **Freshness is per writer, not per directory.** The store is single-writer by design
  (`queue-acp.md`: *the workspace has exactly one writer*), so the advancement is only
  meaningful against a known writer. Two writers make the signal unreadable — which is
  the same rule that keeps the directory from being written by two clusters.
- **mtime is a local clock.** Ordering within a cluster may use it; comparison across
  clusters uses the tick, because `mod(UTC, 5s)` is the clock both sides can compute
  and a local mtime is not.
- **Persistence is not presence.** A store also preserves the record of a node that is
  gone, which is what the accounting wants and what presence must never read. That is
  the register-vs-ledger decision (*Open decisions*), now with a physical
  consequence: the same bytes serve history and would falsely serve liveness.

The result is that a persistent address (`The fabric`) can be checked rather than
trusted: the address persists, the announcement may outlive its author, and the
workspace is where the disagreement is visible.

## Absence is not compromise

*Your neighbours do not change their locks when you return from vacation. They might if
someone broke into your house.*

Two events that look alike from outside — you were not answering — with opposite
consequences:

| event | evidence | peer action |
| --- | --- | --- |
| **vacation** — silence | the record is as you left it | dampen: suppressed, not banned; no key change; resume on return |
| **burglary** — compromise | the record shows writes you did not make, or a chain that no longer verifies | revoke, withdraw the routes, and tell the peers who shared state |

A keepalive cannot tell these apart. A heartbeat that stops and restarts says nothing
about the interval — it is the same silence either way. **A durable filesystem can**,
and that is the stronger reason to keep one: on return, the bytes are either as you
left them or they are not.

Which makes the workspace a **tripwire**, not merely a liveness signal — but only
under two conditions, and without them the burglar tidies up:

- **the writer is identified** (single-writer, per-node sequence), so "a write I did
  not make" is a meaningful sentence;
- **the history is tamper-evident** (sequence plus hash chain), so a rewrite is
  visible rather than merely different.

That is the same chain the plane needs for its audit (`The two logs`), doing a second
job: the store that makes the trajectory recomputable is also the object that
distinguishes *nothing happened* from *something happened that I cannot account for*.

Two corollaries worth keeping:

- **Quiet is not a security event.** An idle node is on vacation; damping handles it
  and nobody re-keys. The thing to act on is quiet *plus evidence of activity*.
- **A break-in re-keys the neighbours, not the world.** Who must act is whoever shared
  the compromised state — the mirror, the surface, the isotope records. That is a
  scoped withdrawal, in the BGP sense (`The fabric`), not a global rotation.

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
- **A reservation that never completes blocks the doorbell.** A prewarmed slot held
  indefinitely is head-of-line blocking for everyone behind it, so reservations expire
  and return to the back of the queue rather than being held silently.
- **A persisted address can outlive its state.** If the peer does not hold the mirrored
  traffic, a role swap behind a stable address is a cold start wearing a warm address:
  the address claim survives, the capability does not.
- **A store that can be rewritten without evidence turns a burglary into a vacation.**
  Durability alone proves nothing about the interval; the sequence and the hash chain
  are what make absence distinguishable from intrusion.
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
