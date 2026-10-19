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

## The shape, in one paragraph

Negotiation is TLS-shaped, with a complex position in place of a certificate chain:
three flights, one per tick, and **three ticks is the minimum tumble**.

The two positions are the same point written in a different order, and the order is the
information:

| position | written | says |
| --- | --- | --- |
| the joining cluster's start | **`42i + 0`** | imaginary-first: a claim of 42, nothing yet verified |
| the public position | **`0i + 42`** | real-first: 42 verified, zero departure |

`42 + 0i` and `0i + 42` are equal in complex arithmetic, so the ordering carries
emphasis rather than value: which component is load-bearing. A joiner arrives carrying
its claim; the cluster's public position is the verified ground truth; the distance
between the two is what the exchange measures, and what the plane plots.

**And the math is not self-declared. It is computed from feature specs the remote tests
you on.** The weights in `MATH.md` — `+8` malformed or unpredictable, `+16` boundary
violation, `+32` unknown tool, `+8` content variation, `+8` scope compliance, `+16`
honeypot contact — are *test outcomes*, not integers to reason from. That is why the
position is observed by the remote and never asserted by the node, and why the isotope
is a signed feature spec: it is the thing the remote can test, and the test is what
moves the position.

## Where `0i` comes from: null, null exists, 0, `0 < 1`

The public position is derived, not assumed, and the derivation is four steps — each
adding exactly one distinction:

| step | adds | what breaks without it |
| --- | --- | --- |
| **null** | no value at all | — |
| **null exists** | absence is representable | a missing measurement is indistinguishable from a good one |
| **0** | zero as a value | a real zero cannot be stated |
| **`0 < 1`** | the first strict order | a departure cannot be measured, and has no direction |

`0i` — *no departure* — only means something after the fourth step. Before it, zero and
unknown are the same symbol. The vault states the discipline directly: *absence is not a
zero value; the "unknown" rung is a compendium with no plane, not a compendium valued at
zero* (`lore/CONVENTIONS.md`), and local-time's lift returns `null` rather than `0` when
a condition cannot be computed (`scripts/build-lore-json.js`, `liftTo42i`).

Which makes the public position the **null hypothesis of the measure**: `0i + 42` is the
only position that costs no evidence. Every other value is a departure that has to be
measured — which is also why anyone can compute it, and why it is not a secret.

### The weights come from local-time, and the two positions are two build states

`local-time` produces the corpus model — conditions, charges, tensor, encoder — and
`isotope/plane` consumes it as embedded weights. The two positions are not a metaphor for
that; they are the two builds:

| build | model | anchor | position |
| --- | --- | --- | --- |
| default (no tag) | generic, corpus-free | `42i` hypothesis state, real part 0 | **`42i + 0`** — the joiner's claim |
| `-tags loreweights` | corpus-derived, from local-time | `42`, proven consensus | **`0i + 42`** — the public position |

`isotope/README.md` says it outright: *"No weights is the `42i`+0 hypothesis state."*
The corpus weights are committed only as a SOPS blob (`plane/lore-weights.enc.json`) and
embedded at ship time, so the labels never enter the tree while the model is in the
binary.

### The derivation is implemented, and it is gated on the contract

This is not aspirational: the vault's indexer does it
(`scripts/qdrant/index_kb.py`, §*Plane space* in its README). For each compendium it
reads the `plane:` frontmatter as a **contract**, and the contract wins:

1. charges from the declared `plane.charges` — currently 15×`+1` and 6×`−1`, a real
   two-sector graph rather than an assumed one;
2. `plane gen --charges <declared> --anchor 42` materialises the skeleton;
3. each compendium's value is `c = real tier + i·(42i distance)`, with the real tier from
   `reality-gradient` (0..1 → 0..42, so 1.0 is consensus) and the imaginary part from
   `zero-status` (`established` → 0, otherwise backfilled);
4. tensor edges come from `plane.tensor`, with unset weights backfilled by the cosine of
   the two embeddings — so the `out == 0` blocks carry the real↔fiction coupling.

The result is cached as `.plane-weights.json` and gated by a `plane_hash` **over the
declared contract**, folded with the doc body only where a backfilled slot reads it. Two
consequences worth stealing:

- **a contract edit re-derives; a prose edit does not churn.** That is the same
  discipline as this document's content-free attestation: bind to the declaration, not
  to the bytes.
- **the same file gates three downstream syncs by signature** — `isotope_weights`,
  `local_time_universes`, `docs_sites` in `.plane-set-state.json`. An unchanged hash
  skips the target. That is *"do you?"* asked of artefacts instead of sessions: same
  contract, same hash, same answer.

Two honest limits of the implementation: the `plane` vector does **not** affect MCP
query results today (fastembed only) — it exists for clients that know the model; and
`GenerateModel` has no contract parser behind it (isotope#7), so the shipped binary needs
the SOPS blob rather than reading frontmatter.

Also, a coincidence not to build on: the plane *dimension* is `2 × #compendia`, which is
42 today because there are 21 compendia. That 42 is corpus size, not the anchor.

**One precision, or the provenance overclaims.** There are two weight systems and only
one is derived:

- the **plane** model's weights come from the corpus contract, through the indexer above;
- the **42i distance** weights — `+8`, `+16`, `+32` in `fortytwointegration.go` — are
  hand-chosen, and `isotope/plane` computes by closed-form arithmetic on indices.

So "the weights come from local-time" is true of the plane and not yet of the failure
table. Letting the derived one vouch for the placeholder one is exactly what `MATH.md`
forbids.

## The weights are not portable — by construction

The model is **corpus-relative**. Charges, tensor, encoder and *the set of planes* all
come from the corpus, and `plane_hash` is a hash over **your** contract. So two parties
do not build the same model; they build two frames.

Concretely, this is not a matter of degree:

- the **dimension is `2 × #compendia`**, so the vectors are not even the same length —
  yours is 42 (21 compendia), mine is whatever mine has;
- the **`names` list is yours**, so element *i* of my vector is a different universe from
  element *i* of yours;
- the **encoder is yours**, so `sort` orders *your* corpus by *your* judgements
  (`reality-gradient`, `zero-status` are editorial positions, not measurements).

Which means **everyone has a different ranking**, and that is the design, not a defect:
`SLA.md` §12 already says selection is *local policy, not shortest path*.
Non-portability is that principle applied to the model.

| travels | does not travel |
| --- | --- |
| the algebra: `c = real + i·(42i distance)`, charges as symmetry labels, the tensor pairing, the holonomy | the weights |
| the names: universe ids | the ordering |
| the derivation rule | the vector positions |

So cross-party comparison has to be **by name and value**, never by vector position —
which is what `plane_universe_id` in the payload is for. Where two corpora do not share
a name there is nothing to compare, and the relation between the frames is a *rotation*,
not an equality (*A private plane is a private rotation*).

### Declared versus backfilled — and that is what local-time is for

The line that decides portability is not "weights or no weights". It is **declared
versus backfilled**:

| slot | who fills it | travels? |
| --- | --- | --- |
| charges, `presupposes`, tensor edges, `reality-gradient`, `zero-status` | the contract (frontmatter) | yes — public, auditable, re-derivable |
| encoder slots the contract leaves empty | the local corpus (backfill) | no |
| tensor weights left `w: unset` | cosine over the local embeddings | no |

The indexer's own rule is *the contract wins; fastembed only backfills slots the contract
leaves empty*. So a hole in the contract is filled by whatever the local corpus says, and
that is local by definition. **Portability is contract completeness**, not a choice of
philosophy: a fully declared contract derives the same weights everywhere, and every hole
is a place where two parties diverge.

That is what `local-time` is for — **sourcing the public form of the weights**: epochs,
conditions (`nominalSpanMs`/`realSpanMs`), keyframes and the `plane:` declarations in one
public, versioned place, so the numbers are *derived from declarations* rather than fitted
to text.

And it gives the criterion a number instead of an argument: **count the backfilled
slots.** Today the corpus declares tensor edges but leaves every `w` unset (its README
says so — "`w: unset` — true for every declared edge in the current corpus"), so the
coupling is 100% backfilled. That is exactly why the ranking does not travel yet. The
count belongs in `.plane-set-state.json` beside `plane_hash`, because it is the
portability score.

If the contract is completed, the first option below stops being "adopt somebody's
corpus" and becomes the much weaker "adopt a published declaration set" — which anyone
can verify and derive from, without inheriting a private text.

Portability is therefore a choice, with local-time as the attempt at the first:

1. **Share the contract.** A *complete* public declaration set — not a canonical corpus —
   makes the derivation reproducible and the rankings coincide. What is still adopted is
   the declarations themselves, which remain judgements.
2. **Share the algebra only.** Everyone keeps a local ranking and compares structure and
   holonomy. That is the BGP answer, and it requires nobody to accept another party's
   editorial position.

And a hash proves nothing across parties: equal hashes mean the contracts match, and
unequal hashes mean the contracts differ — not that either is wrong. So *do you?* across
parties must be asked of `(name, value, structure)`, never of `(hash, rank)`.

**What local-time cannot do** is make the judgements objective. `reality-gradient` and
`zero-status` are editorial positions that become *public claims* when declared — stated,
auditable, comparable — not facts. Two parties can derive identical weights from the same
contract and still disagree about whether the contract is right. That is *agreement is not
correctness* one level up: shared derivation, shared opinion, no shared truth.

## Verification is a question, not an inspection

The remote never reads your internals. It asks a reproducible question:

> *I have these records, and I process them this way. Do you?*

Three parts, and the answer only means something against all three:

| part | what it is | why the remote needs it |
| --- | --- | --- |
| **records** | a digest and a log range (or head) | to know *which* inputs the claim is about |
| **processing** | the formula version and the isotope (feature spec) | to know *how* the answer was derived |
| **result** | the position | to compare |

Agreement is the answer, and disagreement localises itself — which is what makes it
useful rather than merely binary:

| records | processing | result | reading |
| --- | --- | --- | --- |
| same | same | differs | a bug or a false claim: the only case that accuses |
| differs | same | — | you are missing evidence, or we are on different logs |
| same | differs | — | different formula version: reconcile, do not accuse |
| differs | differs | — | unrelated claims; nothing to compare |

**Three answers, not two.** *Do you?* can be answered **"I cannot — I do not have those
records."** That is unverifiable, and unverifiable is not false; merging them is the same
error as merging *not placed yet* with *refused*. The log records which of the three,
because a claim that was never checkable is not a claim that failed.

**And the claim is a commitment, not a disclosure.** "I have these records" is a digest
and a head, not the records themselves; the remote can check the answer without being
shown the traffic. That is the same trade as the content-free attestation: what is proved
is *agreement*, not transparency. It is why the position can be public while the inputs
stay private, and why the isotope is a feature spec rather than a certificate — it is a
question the remote can ask, and the answer is arithmetic.

That is also the whole of the tumble, stated without the ceremony: the flights carry the
three parts, and the only thing that makes the exchange trustworthy is that the remote
does the last step itself.

## Why the attestation is content-free

**The cluster may modify the project.** Any receipt that hashed the tree would be
invalidated by the first write, which would make the attestation expire exactly when
the project is being used.

So bind the attestation to a **key**, not to bytes:

```
attestation = sign_cluster( cluster, project_pubkey, surface, anchor, joined_at )
```

- `anchor` is `42i + 0` — the joiner's starting position (*The shape*), and the only
  number in it. It is the declared starting point,
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

The public position `0i + 42` is the same baseline in every cluster — anyone can
compute it. The trajectory away from it is not shared: the **tick is shared by default**
(below), but the measurement chain is not. `PLANE.md` (§*Axes*, §*Partitions*) makes the
accuracy claim against the cluster's own clock, so a partitioned cluster that retained
internal accuracy remains accurate on reconnection. A project in three clusters has
three readings, not one sum.

The distance is a formula over observable inputs, **not stored state**
(`PLANE.md`, §*The computation*). Clusters can be *compared*; they cannot be *added*,
because addition would assume one formula and one observer chain.

## Intervals are relative; the rotation is the invariant

Between disparate planes **any number of intervals can occur** — clocks differ,
partitions last, queues drain — so counting intervals between two clusters measures the
path taken, not the planes. This replaces the weaker claim that comparison needs the
same slot: agreement on *when* was never the point.

A complex number carries two independent facts, and the design uses both:

| part | quantity | answers |
| --- | --- | --- |
| **modulus** | the 42i distance | how far this plane has departed from nominal |
| **argument** | the rotation | how this plane is turned relative to another |

Rotation composes by multiplication; intervals compose by counting. Only one of the two
is invariant to how many intervals happened to be observed — which is why **the rotation
is the ultimate quantity and the interval is a sampling artefact.**

**The loop is the check, not the pair.** Two planes can always be made to agree by
choosing the rotation between them; two numbers always can be fitted. A loop cannot:
multiply *A → B → C → A* and the product must be the identity. Its departure is the
**holonomy** — the curvature of the space — and no single edge can fake it, because
every plane contributes to the result. `isotope/plane` implements exactly this (`edge`,
`triple`, `holonomy`), and it is the same reason three is the smallest number that
closes a loop: the golden triangle both closes the loop and gives a fault a neighbour to
fall to (`RECONCILE.md`).

So the cross-cluster check is a **loop over per-plane phase**, not a comparison across a
pair: each plane contributes the encoder's `freq`/`phase`/`gain`/`decay`
(`PLANE.md`, §*Projection into the plane*), and the holonomy says whether the set is
consistent. The tick's job is now stated precisely: it is how the phase is *sampled*,
not how planes are compared.

## A private plane is a private rotation — and that is where forward secrecy lives

A cluster's own tick is not a degraded mode. It is a **plane with a rotation of its own
relative to the default plane** — one that need not be published. Read that way, two
properties that looked unrelated are the same property seen from different sides:
partition tolerance (this plane is accurate while it cannot be related to the others)
and forward secrecy (the relation cannot be recovered afterwards).

That also completes the TLS analogy, which was missing its key exchange:

| TLS | here | lifetime |
| --- | --- | --- |
| certificate | the attestation — binds a key to a domain | long |
| (EC)DHE | the **rotation** for this session | one session |
| Finished | possession proof bound to that rotation | one session |

The attestation authenticates; it never protects. The protection is the ephemeral
rotation, and the secret never travels: each side contributes to it and both compose
the same result. (The *angle* is the model — the real secret has to come from a real
group or KDF, because a bare real number carries no security.)

**Four conditions, or the angle is a long-term key wearing a costume:**

1. **Fresh per session.** A rotation derived from the attestation is not ephemeral and
   gives nothing back after a compromise.
2. **Drawn from a key space.** An angle sampled "somewhere in the circle" has no entropy
   budget; the rotation must be a key, with a stated size.
3. **Destroyed after use.** Retaining it means a later compromise decrypts the past —
   which is the one thing forward secrecy is defined against.
4. **Not reusable across sessions.** Resumption spends a *credential*, not a key: a
   pre-baked receipt re-authorises the session but must still re-key it.

### Two rotations, and only one of them is a secret

The tension above dissolves once the two quantities stop sharing a word.

| rotation | derived from | secrecy |
| --- | --- | --- |
| the **observed phase** — the trajectory's argument | the public wheel plus the logged observations | public: recomputable by anyone, later |
| the **session rotation** — the key exchange | fresh randomness | private, and destroyed after use |

The observed phase is public *because the wheel is*. `mod(UTC, 5s)` is a published
yardstick, so a future observer holding the log can reconstruct when each sample was
taken, multiply the loop, and see whether it closes. The holonomy is therefore a
**public check** — which is the point of a public tick, and the reason the accounting is
recomputable rather than merely published.

What must never appear in the log is the session rotation itself. The rule that keeps the
two apart:

> **Log observations, commitments, and verdicts. Never seeds.**

And the trap that destroys both properties at once: **never derive the session rotation
from the tick, or from the observed phase.** The wheel is a sampling reference, not a key
source. A rotation derived from a public grid is public by construction, and it fails
condition 1 without failing any test — the sessions still work, they are simply no
longer forward-secret.

## The tick: a shared phase, not a shared counter

The linear axis has a default source: `mod(UTC, 5s)`. It is public and computable by
anyone, so two clusters that share nothing — no federation, no registry, no key
exchange — still agree on when the current slot is. That is what lets unrelated
clusters speak to each other: they rendezvous on the same five-second slot without
first agreeing on a counter or an authority.

Two things it does and does not buy:

- **Alignment, yes.** A position sampled in cluster A at slot *k* and one sampled in
  cluster B at slot *k* are readings of the same phase, and may be compared. The slot is
  the *easiest* way to establish the rotation, not the only one (*Intervals are
  relative*).
- **Order, no.** `mod` discards the cycle, so slot *k* recurs every five seconds. An
  entry carrying only a slot can be replayed as "this tick" indefinitely — the stale
  credential's failure mode, in time. Freshness needs the absolute timestamp (or a
  cycle count) beside the slot, and the log carries both: absolute time orders, the
  slot compares.

Resolution follows from the wheel: two events inside one five-second slot are the same
tick and cannot be ordered by it. An entry that needs sub-slot ordering carries the
absolute time; the tick is the coordinate, not the clock.

A cluster may run its own tick instead — the federation's heartbeat and announce
intervals are the internal variant (`PLANE.md`, §*Axes*). A plane on a private tick has
no *default* relation to another, so it is comparable only once a rotation between them
has been established; the shared tick is simply the cheapest way to establish one. That
is the partition case, and the plane remains accurate while partitioned.

## Negotiation is a handshake in three ticks

Three ingredients: the tick wheel (a shared phase), the 42i position (the measure),
and three tick cycles (the flights) — the minimum tumble.

| flight | slot | carries | TLS analogue |
| --- | --- | --- | --- |
| 1 | *k* | `42i + 0` (the joiner's claim), the surface requested, the project key, and a commitment to the session rotation | ClientHello / ServerHello — public parameters, no secret |
| 2 | *k+1* | the cluster's attestation: `(key, cluster, surface, anchor)` | Certificate — binds a key to a domain, and names the domain reached |
| 3 | *k+2* | proof of possession, bound to the completed rotation | CertificateVerify / Finished |

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

**The asymmetry with TLS, stated so it is not over-read.** `0i + 42` is a coordinate
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

## The motion sensor: a local chain is not enough

The tamper-evident store is the lock and the door log. A burglar who owns the house can
also own the door log — rewrite it coherently, recompute the chain, leave no trace
inside. **Local evidence is necessary and not sufficient**, because the intruder has
unlimited time with the only copy.

What survives a careful cleanup is that they did not have a chance to stop **every**
form of evidence. The neighbour's mirror, the client's receipt, the peer's
announcement — none of those are inside the house:

| witness | holds | survives |
| --- | --- | --- |
| neighbour's mirror (B of A) | A's traffic log and isotope records | A rewriting its own store |
| the client's receipt | the attestation and the seq it was issued at | the cluster rewriting its own log |
| a peer's announcement | the route and its path | the advertiser withdrawing quietly |
| another cluster | its own attestation copy and plane | a compromise confined to one cluster |

None of this is new machinery. Every object in this document already has a
counterparty holding a copy: the mirror (`RECONCILE.md`), the receipt
(*Who pays the fifteen seconds*), the announcement (*The fabric*), the per-cluster plane
(*Why there is one plane per cluster*). The motion sensor is the observation that each of
them is **also evidence** — and the unit of evidence is the pair, not the record.

**Disagreement between two internally consistent records is the signal.** `MATH.md`
warns about a figure that is "internally consistent, externally false"; the witness
turns that into the detectable case — internally consistent, *externally contradicted*.
The rewrite is invisible from inside and obvious from outside, which is the same
inversion as *no node reports on itself*.

Four conditions, or the sensor is decoration:

- **Independence is a failure domain, not a count.** Two processes on one host share the
  host's compromise; that is the same reason stacking spends the isolation property
  (*The fabric*). Strength increases process → host → cluster → trust domain.
- **A sensor that nobody reads did not fire.** Someone has to compare: C verifies B, the
  client compares the head on return, the peer checks the path. Verification cadence is
  part of the mechanism, not an operational afterthought.
- **A trigger is not a verdict.** Motion sensors fire for the cat: a restart, a role
  swap, a clock divergence. Disagreement opens verification — suppressed and re-checked
  rather than banned, exactly as dampening does.
- **Some things cannot be witnessed.** A decision that exists in one place only — a
  private key's use, an unlogged local judgement — has no counterparty, so no trust
  claim should rest on it.

And it sharpens the earlier corollary: the neighbours who must re-key after a break-in
are also the witnesses who make the break-in visible at all. The cost of a compromise is
paid in the same graph that would detect it.

## Fast to revoke, slow to re-approve

*When a house is on fire the meter can be disconnected. But even if the electrics are
perfectly fine, an electrician has to recertify before the power company touches it
again.*

The asymmetry is deliberate, because the two actions are not symmetrical claims:
**cutting is always safe; restoring is a claim about the future.** So:

- **Disconnect is unilateral and evidence-light.** Any peer may withdraw a route, drop a
  session, or suppress a participant on suspicion — the mirror already gives it the
  evidence to justify it, and refusing to serve cannot harm anyone. This is BGP
  withdrawal, self-demotion, and dampening, and none of them needs agreement.
- **Reconnection needs a certifier, and the node is not its own electrician.** Standing
  belongs to a third party with something at stake (the neighbours, C in the triad, the
  cluster that issued the attestation). A node's assertion that it is fine is exactly
  the self-report the triad exists to refuse.

**The meter sits upstream.** The disconnect point is outside the occupant's control,
which is the same reason verification is never self-administered: whoever can cut the
supply does not have to ask the house.

Two restore paths, and the evidence class chooses — this is where the analogy corrects
BGP:

| class | signal | restore |
| --- | --- | --- |
| **noise** — a flap, a restart, the cat | dampening penalty | returns as the penalty decays; no inspection |
| **compromise** — the burglar, the fire | evidence, not variance | recertification by a third party — a fresh isotope and a quorum (*Recertification is a fresh isotope*); **decay is not recertification** |

BGP lets a dampened route return on time alone. That is right for a flap and wrong for
a break-in: waiting is not an inspection, and an intruder benefits from a clock.

**Recertification issues a new attestation, it does not un-revoke the old one.** The
restored session carries a new sequence, a new `joined_at`, and names the certifier;
the revoked receipt stays revoked forever, with both events in the log —
`revoked(seq, by, cause)` then `recertified(seq, by, scope)`. That is what a ledger buys
and a register cannot show, and it is the argument for the ledger in *Open decisions*.

Two consequences that tighten earlier sections:

- **Prewarm cannot pre-pay recertification.** It warms presence and surface
  (*A, B, C, and the doorbell*); authority is the electrician's visit, and it happens at
  use-time or not at all.
- **The cold path is not a bug to optimise.** Its three slots are the inspection. The
  fast path exists for receipts that were never revoked, not for ones that were; a
  revoked receipt that arrives pre-baked is simply a disconnected meter presented for
  reconnection.

## Recertification is a fresh isotope, not a decayed one

This is what the Gherkin isotopes are for. A revoked participant is re-enabled by
**proving it will not burn the house down** — declaring the behaviour it promises,
letting independent verifiers run it, and letting consensus accept the result. The
electrician's visit is a passing spec, not a waiting period.

The flow, and the reason each part is load-bearing:

| part | says | fails without it |
| --- | --- | --- |
| **the isotope** — Gherkin canonicalised and signed | *what* you promise: the feature set and the refusals | a promise nobody can reproduce or compare |
| **a fresh challenge** bound into the run | *when* you proved it | an old pass re-certifies forever |
| **independent verifiers + consensus** | *who* accepted it | self-certification |

**Replay is the hazard, and canonicalisation is what creates it.** The isotope's virtue
is stability — the same spec yields the same token — and that stability is exactly what
makes an old passing token presentable as a new certificate. So the token alone must
never re-enable anything: the challenge is bound into the run, and only the *result* of
that run enters a **new** attestation. A canon token is a claim about behaviour; it is
not a claim about now.

**The recertificate names the new feature combination**, because isotopes are
feature-driven (`The fabric`, communities). That is what makes the difference legible:
if the fix changed what the node does, the spec — and therefore the token — differs, and
the record shows *what changed since the fire* rather than only that something did.
And because the isotope is canonicalised and re-derived rather than trusted, a
recertification cannot be forged any more than an advertisement can.

**Consensus sets the price, and three nodes is not a quorum.** `N ≥ 3F + 1` gives
`F = 0` at `N = 3` (`MATH.md`), so the verification triad can certify only under an
honest-majority assumption. If re-approval is meant to survive a dishonest certifier,
the certifying set has to be larger than the triad — the quorum is a *plural*
electrician, and a single inspection is exactly what the analogy does not permit.

**And "aging back in" is the right instinct with one correction:** it does not age, it
*argues*. Time is still not an inspection; the isotope replaces the clock with evidence.
So the old receipt stays revoked, and the restored session carries a new attestation
citing the isotope and the challenge that produced it.

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
anyone can compute `0i + 42`, re-run the projection, and read the current five-second
slot. The **inputs** are not — a traffic log is who-called-what.

So publish the derived position plus a hash of the log head. A verifier can then check
that history was not rewritten without reading the traffic. That is the same split as
`SPLASH.md` §*Two invariants*: the traffic log and the isotope records are the audit
surface, not the advertisement.

Because the wheel is public, the loop is too: an observer with the log and the clock can
recompute the holonomy and check that it closes (*Two rotations*). That is the strongest
form of the claim — the accounting is not merely published, it is **re-derivable by
someone who was not there**.

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
- **A witness inside the same failure domain is a mirror, not a witness.** Two processes
  on one host, or two clusters on one key, share the compromise and corroborate
  nothing; independence is what the evidence is made of.
- **A revoked receipt never ages back into validity.** Decay restores a dampened route;
  it does not restore a disconnection. Restoring requires a new attestation from a
  certifier, because time is not an inspection.
- **A passing spec is not a passing run.** A canonical isotope is stable by design, so
  the token proves the promise and not the moment; without a fresh challenge bound into
  the run, an old pass re-certifies forever.
- **Agreement is not correctness.** *Do you?* detects divergence, not error: two nodes
  that share a bug share an answer, and a shared formula bug is exactly the figure
  `MATH.md` calls "internally consistent, externally false". Consensus bounds who can
  lie, never whether the arithmetic is right.
- **A pair is always reconcilable; only a loop constrains.** Any two planes can be made
  consistent by choosing the rotation between them, so a pairwise check approves
  everything. The holonomy of a three-plane loop is the first check that can fail — and
  it only fails if all three were measured, which is why no edge may be skipped.
- **An angle is not a key.** "A rotation somewhere on the circle" has no entropy
  budget; forward secrecy starts when the rotation is drawn from a key space, with a
  stated size, and ends the moment it is retained or derived from the attestation.
- **Auditability is a logging requirement.** The public wheel only lets a stranger
  recompute the holonomy if the log carries the sampling instant, the observer, the
  observation, and the commitment. Omit any of them and the audit degrades to trusting
  whoever wrote the log.
- **A derived weight table does not vouch for a placeholder one.** The plane's weights
  come from the corpus; the distance weights are hand-chosen. Any claim should say which
  table it rests on, because the second cannot inherit the first's provenance.
- **A hash gate proves the input did not change, not that the derivation is right.** The
  `plane_hash` catches drift and staleness; it cannot catch a wrong projection, because
  the same derivation applied to the same contract produces the same wrong answer.
- **A hash is also not a shared identity.** It is over *your* contract, so equal hashes
  are a coincidence of text and unequal hashes are the normal case; portability is a
  choice of shared contract or shared algebra, never something a digest supplies.
- **Portability is contract completeness, not objectivity.** A complete declaration set
  makes the derivation reproducible; it does not make its editorial inputs true. Count
  the backfilled slots, and do not mistake zero holes for zero judgement.
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

1. **Does existence survive absence — and does revocation?** A **register** records events
   only (*this key was placed here at seq n*); a **ledger** records state changes too
   (*revoked at n*, *recertified at m*), so "currently authorised" is derived rather
   than assumed. *Fast to revoke, slow to re-approve* argues for the ledger: a register
   cannot show that a receipt was revoked, or that a different one was certified after
   it.
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
6. **What is the commitment format for the public loop?** The holonomy is publicly
   recomputable from the wheel and the log (*Two rotations*), so the check itself is not
   the secret. What remains open is the shape of the commitments the log carries, so a
   verifier can confirm the observations are the ones the session used — without the log
   ever holding a seed.

## See also

- [seed.md](seed.md) — the `seed.json` schema, and identity vs placement.
- [surfaces.md](surfaces.md) — what a surface is, and what joining one means.
- `lab-safety/lab/MATH.md` — which figures are grounded, and which are placeholders.
- `lab-safety/lab/PLANE.md` — the measure as a plane, and why it is computed.
- `lab-safety/lab/SPLASH.md` — the trust model, probes, and the two invariants.
