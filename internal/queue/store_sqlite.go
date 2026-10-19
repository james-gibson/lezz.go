package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, same choice as lab-safety's workspaces
)

// sqliteStore is the durable mirror. One connection and WAL, like
// workspaces.SQLite: a single writer, batch commits, no per-job fsync.
type sqliteStore struct {
	db   *sql.DB
	path string
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS jobs (
  id              TEXT PRIMARY KEY,
  args            TEXT NOT NULL,
  priority        INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL,
  timeout_seconds INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS results (
  id          TEXT PRIMARY KEY,
  output      TEXT,
  exit_code   INTEGER NOT NULL DEFAULT 0,
  success     INTEGER NOT NULL DEFAULT 0,
  error       TEXT,
  version     TEXT,
  queued_at   TEXT,
  started_at  TEXT,
  finished_at TEXT,
  batch_size  INTEGER NOT NULL DEFAULT 0,
  wait_ms     INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS journal (
  seq     INTEGER PRIMARY KEY AUTOINCREMENT,
  id      TEXT,
  payload BLOB,
  at      TEXT NOT NULL
);
`

func newSQLiteStore(path string) (*sqliteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("queue: sqlite dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("queue: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("queue: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("queue: sqlite schema: %w", err)
	}
	return &sqliteStore{db: db, path: path}, nil
}

func (s *sqliteStore) Name() string { return "sqlite" }
func (s *sqliteStore) Close() error { return s.db.Close() }

func (s *sqliteStore) PutJob(ctx context.Context, job *Job) error {
	args, err := json.Marshal(job.Args)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO jobs(id, args, priority, created_at, timeout_seconds)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  args=excluded.args, priority=excluded.priority,
		  created_at=excluded.created_at, timeout_seconds=excluded.timeout_seconds`,
		job.ID, string(args), job.Priority, formatTime(job.CreatedAt), job.TimeoutSeconds)
	return err
}

func (s *sqliteStore) ListJobs(ctx context.Context) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, args, priority, created_at, timeout_seconds
		FROM jobs ORDER BY priority DESC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]*Job, 0)
	for rows.Next() {
		var (
			j       Job
			rawArgs string
			created string
		)
		if err := rows.Scan(&j.ID, &rawArgs, &j.Priority, &created, &j.TimeoutSeconds); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(rawArgs), &j.Args)
		j.CreatedAt = parseTime(created)
		out = append(out, &j)
	}
	return out, rows.Err()
}

func (s *sqliteStore) DeleteJob(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id=?`, id)
	return err
}

func (s *sqliteStore) PutResult(ctx context.Context, res *Result) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO results(id, output, exit_code, success, error, version,
		  queued_at, started_at, finished_at, batch_size, wait_ms)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  output=excluded.output, exit_code=excluded.exit_code, success=excluded.success,
		  error=excluded.error, version=excluded.version, queued_at=excluded.queued_at,
		  started_at=excluded.started_at, finished_at=excluded.finished_at,
		  batch_size=excluded.batch_size, wait_ms=excluded.wait_ms`,
		res.ID, res.Output, res.ExitCode, boolInt(res.Success), res.Error, res.Version,
		formatTime(res.QueuedAt), formatTime(res.StartedAt), formatTime(res.FinishedAt),
		res.BatchSize, res.WaitMS)
	return err
}

func (s *sqliteStore) GetResult(ctx context.Context, id string) (*Result, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, output, exit_code, success, error, version,
		  queued_at, started_at, finished_at, batch_size, wait_ms
		FROM results WHERE id=?`, id)
	res, err := scanResult(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return res, err
}

func (s *sqliteStore) ListResults(ctx context.Context) ([]*Result, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, output, exit_code, success, error, version,
		  queued_at, started_at, finished_at, batch_size, wait_ms
		FROM results`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]*Result, 0)
	for rows.Next() {
		res, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

func (s *sqliteStore) AppendCommit(ctx context.Context, job *Job, res *Result) error {
	payload, err := json.Marshal(map[string]any{"job": job, "result": res})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO journal(id, payload, at) VALUES(?, ?, ?)`,
		res.ID, payload, formatTime(time.Now().UTC()))
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanResult(s scanner) (*Result, error) {
	var (
		res                       Result
		success                   int
		queued, started, finished string
	)
	if err := s.Scan(&res.ID, &res.Output, &res.ExitCode, &success, &res.Error, &res.Version,
		&queued, &started, &finished, &res.BatchSize, &res.WaitMS); err != nil {
		return nil, err
	}
	res.Success = success != 0
	res.QueuedAt = parseTime(queued)
	res.StartedAt = parseTime(started)
	res.FinishedAt = parseTime(finished)
	return &res, nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
