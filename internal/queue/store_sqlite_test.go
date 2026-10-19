package queue

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteMirrorParity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mirror string
		store  string
	}{
		{name: "file", store: "file"},
		{name: "file+sqlite", store: "file+sqlite"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mirror := ""
			if tc.name == "file+sqlite" {
				mirror = filepath.Join(t.TempDir(), "queue.db")
			}
			q := testQueue(t, Config{
				MaxBatch: 5, MaxWait: "30ms", RatePerSec: 1000, Burst: 5,
				Concurrency: 1, Upstream: "fake", SQLiteMirror: mirror,
			})
			if got := q.store.Name(); got != tc.store {
				t.Fatalf("store = %q, want %q", got, tc.store)
			}
			ids := enqueueN(t, q, 10)
			waitAll(t, q, ids, 10*time.Second)
			if q.completed.Load() != 10 {
				t.Fatalf("completed=%d", q.completed.Load())
			}
			q.mu.Lock()
			batches := append([]int(nil), q.batchLog...)
			q.mu.Unlock()
			if len(batches) != 2 {
				t.Fatalf("batches=%v", batches)
			}
		})
	}
}

func TestMirrorReplaysSQLiteOnlyJob(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "queue.db")
	sq, err := newSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "sqlite-only", Args: []string{"x"}, CreatedAt: time.Now().UTC()}
	if putErr := sq.PutJob(context.Background(), job); putErr != nil {
		t.Fatal(putErr)
	}
	if closeErr := sq.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	q, err := New(Config{
		Name: "replay", Dir: dir, Listen: "127.0.0.1:0",
		MaxBatch: 5, MaxWait: "20ms", RatePerSec: 1000, Burst: 5,
		Concurrency: 1, Upstream: "fake", SQLiteMirror: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if startErr := q.Start(context.Background()); startErr != nil {
		t.Fatal(startErr)
	}
	defer func() { _ = q.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := q.Wait(ctx, "sqlite-only")
	if err != nil {
		t.Fatalf("sqlite-only job was not replayed: %v", err)
	}
	if !res.Success {
		t.Fatalf("replayed job failed: %+v", res)
	}
}

func TestMirrorSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "queue.db")
	cfg := Config{
		Name: "restart", Dir: dir, Listen: "127.0.0.1:0",
		MaxBatch: 5, MaxWait: "5s", RatePerSec: 1000, Burst: 5,
		Concurrency: 1, Upstream: "fake", SQLiteMirror: db,
	}
	q, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if startErr := q.Start(context.Background()); startErr != nil {
		t.Fatal(startErr)
	}
	ids := enqueueN(t, q, 3)
	if closeErr := q.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	// The worker was lingering, so the jobs should still be on disk and in
	// sqlite. A fresh queue must replay/process them.
	cfg.MaxWait = "20ms"
	q2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := q2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q2.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, id := range ids {
		if _, err := q2.Wait(ctx, id); err != nil {
			t.Fatalf("job %s did not survive restart: %v", id, err)
		}
	}
}

func TestMirrorReportsTornResults(t *testing.T) {
	dir := t.TempDir()
	fs, err := newFileStore(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	sq, err := newSQLiteStore(filepath.Join(dir, "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	m := newMirrorStore(fs, sq)
	defer func() { _ = m.Close() }()

	ctx := context.Background()
	if putErr := fs.PutResult(ctx, &Result{ID: "x", Output: "file", Success: true}); putErr != nil {
		t.Fatal(putErr)
	}
	if putErr := sq.PutResult(ctx, &Result{ID: "x", Output: "sqlite", Success: true}); putErr != nil {
		t.Fatal(putErr)
	}
	torn, err := m.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(torn) != 1 || torn[0] != "x" {
		t.Fatalf("torn = %v, want [x]", torn)
	}
}
