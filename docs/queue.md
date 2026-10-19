# Queue sidecar: spool + FIFO doorbell

See [queue-acp.md](queue-acp.md) for the single-writer package model, the ACP
adapter, and the SQLite mirror required by lab-safety#5. The read-side
counterpart to this queue is [`lezz cache`](pr-cache.md): PR view/diff cached by
`repo#number#kind`, single-flight misses, stale-on-error.

`lezz queue` puts a bounded, durable queue in front of an expensive or
rate-limited upstream. It is the "redirect a mesh stream through a pipe"
shape: the mesh keeps HTTP for discovery and control, while this one data
plane goes through a local spool.

## Why files *and* a pipe

A FIFO is a byte stream. It gives wakeup and backpressure, but it cannot be
peeked, sorted, batched, or recovered after a crash. So:

| Path | Role |
|---|---|
| `jobs/<id>.json` | the queue of record (atomic write, survives restart) |
| `doorbell` (FIFO) | one byte per enqueue — wakeup only |
| `results/<id>.json` | completion record — the "update when it's done" |
| `dlq/<id>.json` | failed jobs, kept for inspection |

The worker polls the FIFO with `unix.Poll`; a ticker is the safety net. The
spool is the source of truth, so a missed doorbell is harmless.

## Config

```yaml
name: github
dir: ~/.lezz/queues/github
listen: 127.0.0.1:0
max_batch: 5          # select at most 5 jobs per drain
max_wait: 250ms       # linger to fill a batch
rate_per_sec: 5       # token bucket
burst: 5
concurrency: 1        # serial upstreams; 5 for independent gh calls
max_depth: 50         # enqueue past this -> 429, not unbounded memory
fsync: false          # true only if power-loss durability matters
sqlite_mirror: ~/.lezz/queues/github/queue.db  # durable second driver
upstream: gh
command: gh
```

`fsync: true` costs ~7 ms per job on this machine. With it off, enqueue is
~0.18 ms/job; `rename` is still atomic against process crashes. The SQLite
mirror uses WAL + `synchronous=NORMAL` + one connection, so durability is
amortised over a batch instead of paid per job.

## Store drivers

The worker writes through a `Store`, not a directory:

| Driver | Role |
|---|---|
| `file` | today's `jobs/` + `results/` + `dlq/` spool; human-inspectable |
| `sqlite` | durable mirror (`modernc.org/sqlite`, WAL, one connection) |
| `file+sqlite` | both, with the file driver as primary and reconcile on start |

`AppendCommit` records each completion in `journal.jsonl` (file) and the
`journal` table (sqlite). On start the mirror compares ids in both directions:
a job missing on one side is replayed; results that disagree are reported as
`torn` on `/status` and in the log, and neither side is treated as the winner.
That is the lab-safety#5 rule, not a last-write merge.

Tests run the same batch/rate/doorbell/package scenarios against `file` and
`file+sqlite` (`TestSQLiteMirrorParity`), plus replay, restart, and the torn
case.

Live check (5 × `gh api rate_limit`):

```
store=file+sqlite  depth=0  completed=5  batches=1  batch_sizes=[5]  torn=0
sqlite: results=5  journal=5  jobs=0
```

## HTTP

```
POST /enqueue   {"args":["api","rate_limit"],"wait":true}
GET  /result/<id>
GET  /status     depth, oldest_age_ms, in_flight, completed, failed,
                 batches, batch_sizes, last_rate_wait_ms, dropped
GET  /healthz
```

`wait:true` returns the completion record (`output`, `exit_code`, `success`);
`wait:false` returns `{id, status:"queued"}` for async callers.

## adhd wiring

`adhd.gh.run` keeps its response shape. With `ADHD_GH_QUEUE_URL` set, it
enqueues and waits instead of spawning `gh` itself:

```sh
lezz queue --config ~/.lezz/queues/github.yaml
ADHD_GH_QUEUE_URL=http://127.0.0.1:PORT adhd --headless --mcp-addr 127.0.0.1:9090
```

The queue owns the process and the token; adhd never sees either.

## Measured (2026-10-19)

| Scenario | Result |
|---|---|
| direct sequential, 20 × `gh api rate_limit` | **8.70 s** |
| queue, concurrency 5, batch 5, 5/s | **3.51 s**, 20/20 success, batches `[1,5,5,5,5]` |
| enqueue 20 jobs | **13.2 ms** |
| direct under 5/s quota, fail-fast | 5 ok / **15 rejected** |
| fake batch API (5 × 100 ms) vs direct (20 × 50 ms) | **0.635 s** vs **1.02 s** |
| prior fanout probe, n=10 (sequential) | 0.50 s; 50 ms ticker → **~2.5 Hz** |
| prior Qdrant parallel store | 1 success + 4 × 409; serial queue 5/5 |

The queue does not beat a quota; it decides who waits and who is shed, and it
lets the producer return immediately. Batching only helps when the upstream
has a real batch path or when serialization is required (the Qdrant create
case). Otherwise it is a concurrency cap plus a rate limiter.

## Limits

- Local only. Cross-host needs a queue per host or a network queue.
- One worker per spool. Multiple readers on a FIFO split bytes, not messages.
- The `gh` upstream inherits the worker's environment; the worker is a trusted
  lab process. A token-scoped env allow-list is the next hardening step.
- The HTTP listener is loopback with no auth. Do not expose it; a unix socket
  or a per-queue token is the follow-up.
- `fsync: false` survives process crash, not power loss.
