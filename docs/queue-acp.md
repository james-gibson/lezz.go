# One writer, three adapters: package.json, ACP, MCP, and the queue

This document pins the boundary between the single-writer package model, the
lab's ACP agent, the MCP surfaces, and the queue sidecar. The rule is simple:

> **The workspace has exactly one writer. Package logic lives in one core.
> ACP, MCP, and the queue are adapters — none of them reimplement intent,
> versioning, merging, or the lockfile.**

The queue mechanics are in [queue.md](queue.md). This file is about not
duplicating them per protocol.

## The core

```go
type Intent struct {
    ID        string
    Op        string // add-dep, remove-dep, set-script, set-field, merge
    Path      string // dependencies.react, scripts.test, engines.node
    Value     any
    IfVersion string // optional optimistic-concurrency guard
}

type Commit struct {
    Version   string // hash/seq of package.json + lockfile
    Diff      string
    Applied   []Intent
    Conflicts []Conflict
}

type PackageWriter interface {
    Read(ctx context.Context) (body []byte, version string, err error)
    Request(ctx context.Context, in Intent) (*Commit, error) // enqueue + wait
    Subscribe(ctx context.Context) (<-chan Commit, error)    // optional push
}
```

- `Read` is lock-free and returns the committed snapshot + version. Five readers
  never block each other.
- `Request` is the only mutation path. It enqueues; the single writer applies,
  commits `package.json` and `package-lock.json` together, and returns the
  version and diff.
- `IfVersion` stale → `ErrConflict{CurrentVersion, Diff}`. Without it, agents
  that read and compute changes are still racing; the queue just moves the race.
- The writer owns the expensive step: `npm install --package-lock-only` once per
  batch, not once per intent. Use `tailscale/hujson` (already a lezz dependency)
  to preserve comments and formatting; apply semantic ops, not text patches.
- Merge only commuting classes: different `dependencies` keys merge; same-key
  edits and `scripts`/`engines`/`workspaces` conflicts are rejected with the
  current value and diff. Never silently last-write.

## ACP adapter (lab-safety `workspace-agent`)

The lab agent is deliberately minimal (`lab/agent/agent.go:1-16`): ACP v1, the
four required methods — `initialize`, `session/new`, `session/prompt`,
`session/cancel` — and **no permissions, no client fs/terminal calls, no v2**.
A prompt is a command executed against the session store and streamed back as a
tool call.

The package adapter is therefore a **new verb**, not a new ACP method:

```
package add react@^18
package remove zod
package set scripts.test "vitest run"
```

- `parse`/`exec` (`agent.go`) route the verb to `PackageWriter.Request`.
- `Prompt` already wraps the command in `StartToolCall` / `CompleteToolCall` /
  `SendText` (`session/update`), and returns a `PromptResponse`. That is the
  completion notification — no new ACP notification type.
- Do **not** route package edits through the existing `write` verb or
  `driver.Put`: a plain write bypasses the writer's versioning, merging, and
  lockfile transaction (and, per the agent's own note, the recorder).
- If the editor's client owns the file through `fs/write_text_file`, that is a
  **second writer**. Pick one per workspace. The lab agent's no-client-fs stance
  means the queue/store is the writer; the agent sends intents via prompts.
- `session/request_permission` is not implemented today. If it is added, it
  gates the intent (approval) and then calls the same `Request`. It does not
  replace the writer's conflict policy.

## MCP adapter

- `workspace.package.edit` (tools/call) → `Request`; `workspace.package.read` →
  `Read`. The result includes `version` and `diff`.
- `adhd.project.<name>.call` can reach it when adhd is the MCP client; it is an
  ordinary HTTP MCP target, no new transport.
- adhd#27 still applies: an unentitled caller must not reach the write adapter.
  The read adapter is safe to expose; the write adapter is not.

## Queue adapter

- `Request` = `queue.Enqueue` + `Wait` (or return a job id for async callers).
- `max_batch` = merge commuting intents into one commit; `concurrency: 1`;
  `rate_per_sec: 0` (the commit cost is `npm install`, not JSON); `max_depth`
  is the shed/backpressure point.
- The queue's `Result` becomes the `Commit`: version, diff, applied, conflicts.
- Notification is the result record (`results/<id>.json`, durable), an SSE
  `/events` stream, or a `done` FIFO. One record, three readers; do not invent a
  fourth per protocol.

## SQLite mirror (lab-safety#5)

The VFS hypothesis is **one writer, two drivers, the same suite unchanged on
each** (`lab/workspaces/driver.go:78-100`: `Mkdir/Put/Get/Delete/Exists/List/
Name/Close`; #5 and the #9 spike). The queue should follow the same shape.

Extract a `Store` from the current file spool:

```go
type Store interface {
    PutJob(ctx context.Context, *Job) error
    ListJobs(ctx context.Context) ([]*Job, error)
    DeleteJob(ctx context.Context, id string) error
    PutResult(ctx context.Context, *Result) error
    GetResult(ctx context.Context, id string) (*Result, error)
    ListResults(ctx context.Context) ([]*Result, error)
    AppendCommit(ctx context.Context, *Job, *Result) error
    Name() string
    Close() error
}
```

- `fileStore` is today's `jobs/` + `results/` + `dlq/` spool.
- `sqliteStore` is the durable mirror. The worker writes both; the queue suite
  runs unchanged on either. The writer is still one.
- Schema (SQLite, `modernc.org/sqlite` as the lab already uses):

```sql
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=5000;

CREATE TABLE jobs (
  id TEXT PRIMARY KEY, args TEXT NOT NULL, priority INTEGER,
  created_at TEXT NOT NULL, state TEXT NOT NULL
);
CREATE TABLE results (
  id TEXT PRIMARY KEY, output TEXT, exit_code INTEGER, success INTEGER,
  error TEXT, version TEXT, started_at TEXT, finished_at TEXT, batch_size INTEGER
);
CREATE TABLE commits (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, version TEXT, diff TEXT, applied_at TEXT
);
CREATE TABLE journal (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT, id TEXT, payload BLOB, at TEXT
);
```

- One connection (`SetMaxOpenConns(1)`), WAL, batch commits. Durability without
  per-job `fsync`: the file spool measured **7.4 ms/job** synced and **0.18 ms**
  unsynced; a WAL transaction amortises the fsync over a batch.
- Keep the FIFO doorbell. SQLite in this setup has no LISTEN/NOTIFY; the store is
  the queue of record, the pipe is the wakeup.
- **Reconciliation, not last-write:** on start, compare ids in file and sqlite.
  A job missing in one → replay it. A result that differs → mark `torn` and do
  not pick a winner. That is the mirror-disagreement the VFS spike is about, and
  it is the same positional-independence rule: do not trust either side's
  bookkeeping alone.

## What not to duplicate

- No second package editor in the ACP agent, the MCP server, or the queue
  upstream. One `PackageWriter`.
- No second completion mechanism per surface. The `Commit`/`Result` is the one
  record; ACP streams it as a tool call, MCP returns it, the queue stores it.
- No second writer. The editor client or the queue owns the file, never both.
- No second storage policy. The file spool and the SQLite mirror are drivers of
  the same `Store`; the suite must pass unchanged on each.

## Status

- Implemented today: the queue sidecar (`lezz queue`), file spool + FIFO
  doorbell, the `gh` upstream, `ADHD_GH_QUEUE_URL`, the five-agent
  single-writer measurement (`internal/queue/package_test.go`), and the
  `Store` extraction with the SQLite mirror (`file` and `file+sqlite`;
  parity, replay, restart, and torn tests).
- Designed here, not yet implemented: the `PackageWriter` core, the ACP
  `package` verb, and the MCP `workspace.package.*` tools. The interfaces above
  are the contract those changes should meet.
