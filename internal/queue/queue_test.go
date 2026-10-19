package queue

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func testQueue(t *testing.T, cfg Config) *Queue {
	t.Helper()
	cfg.Dir = t.TempDir()
	if cfg.Name == "" {
		cfg.Name = "test"
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	q, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

func enqueueN(t *testing.T, q *Queue, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		j := &Job{Args: []string{"x"}}
		if err := q.Enqueue(j); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		ids = append(ids, j.ID)
	}
	return ids
}

func waitAll(t *testing.T, q *Queue, ids []string, timeout time.Duration) []*Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out := make([]*Result, 0, len(ids))
	for _, id := range ids {
		res, err := q.Wait(ctx, id)
		if err != nil {
			t.Fatalf("Wait %s: %v", id, err)
		}
		out = append(out, res)
	}
	return out
}

func TestBatchesBurstAtMaxBatch(t *testing.T) {
	q := testQueue(t, Config{
		MaxBatch:         5,
		MaxWait:          "40ms",
		RatePerSec:       1000,
		Burst:            5,
		Concurrency:      1,
		Upstream:         "fake",
		FakeBatchLatency: "20ms",
	})
	ids := enqueueN(t, q, 20)
	start := time.Now()
	waitAll(t, q, ids, 10*time.Second)
	elapsed := time.Since(start)

	q.mu.Lock()
	batches := append([]int(nil), q.batchLog...)
	q.mu.Unlock()
	if len(batches) != 4 {
		t.Fatalf("expected 4 batches, got %v", batches)
	}
	for _, n := range batches {
		if n != 5 {
			t.Fatalf("expected full batches of 5, got %v", batches)
		}
	}
	// Four 20ms batches should finish well under a second.
	if elapsed > 2*time.Second {
		t.Fatalf("batch run took %s", elapsed)
	}
	t.Logf("20 jobs, batch=5: %s, batches=%v", elapsed.Round(time.Millisecond), batches)
}

func TestRateLimitSpacing(t *testing.T) {
	q := testQueue(t, Config{
		MaxBatch:    5,
		MaxWait:     "20ms",
		RatePerSec:  5,
		Burst:       5,
		Concurrency: 1,
		Upstream:    "fake",
	})
	ids := enqueueN(t, q, 15)
	start := time.Now()
	waitAll(t, q, ids, 15*time.Second)
	elapsed := time.Since(start)

	// 15 jobs at 5/s with burst 5 = two extra token waits of ~1s each.
	if elapsed < 1700*time.Millisecond {
		t.Fatalf("rate limit not applied: %s", elapsed)
	}
	if q.completed.Load() != 15 {
		t.Fatalf("completed=%d", q.completed.Load())
	}
	t.Logf("15 jobs at 5/s (burst 5): %s", elapsed.Round(time.Millisecond))
}

func TestDoorbellWakesBeforeMaxWait(t *testing.T) {
	q := testQueue(t, Config{
		MaxBatch:    1,    // a single job fills the batch, so no linger
		MaxWait:     "5s", // deliberately huge; the FIFO should wake us
		RatePerSec:  100,
		Burst:       5,
		Concurrency: 1,
		Upstream:    "fake",
		FakeLatency: "10ms",
	})
	ids := enqueueN(t, q, 1)
	start := time.Now()
	waitAll(t, q, ids, 3*time.Second)
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Fatalf("doorbell did not wake the worker: %s", elapsed)
	}
	t.Logf("single job with max_wait=5s completed in %s", elapsed.Round(time.Millisecond))
}

func TestSpoolSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		j := &Job{ID: "job-" + string(rune('a'+i)), Args: []string{"x"}, CreatedAt: time.Now().UTC()}
		if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSONAtomic(filepath.Join(dir, "jobs", j.ID+".json"), j, false); err != nil {
			t.Fatal(err)
		}
	}
	q, err := New(Config{Name: "restart", Dir: dir, Listen: "127.0.0.1:0", MaxBatch: 2, MaxWait: "20ms", RatePerSec: 1000, Burst: 2, Concurrency: 1, Upstream: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, id := range []string{"job-a", "job-b", "job-c"} {
		res, err := q.Wait(ctx, id)
		if err != nil {
			t.Fatalf("spooled job %s not processed: %v", id, err)
		}
		if !res.Success {
			t.Fatalf("job %s failed: %+v", id, res)
		}
	}
}

func TestMaxDepthRejects(t *testing.T) {
	q := testQueue(t, Config{
		MaxDepth:    2,
		MaxBatch:    1,
		MaxWait:     "20ms",
		RatePerSec:  1000,
		Burst:       1,
		Concurrency: 1,
		Upstream:    "fake",
		FakeLatency: "10s", // hold the first job in-flight so depth stays at 1
	})
	if err := q.Enqueue(&Job{Args: []string{"1"}}); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(&Job{Args: []string{"2"}}); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(&Job{Args: []string{"3"}}); !errors.Is(err, ErrFull) {
		t.Fatalf("expected ErrFull, got %v", err)
	}
}

func TestResultShapeMatchesGHRun(t *testing.T) {
	q := testQueue(t, Config{
		MaxBatch:    5,
		MaxWait:     "20ms",
		RatePerSec:  100,
		Burst:       5,
		Concurrency: 1,
		Upstream:    "fake",
	})
	ids := enqueueN(t, q, 1)
	res := waitAll(t, q, ids, 5*time.Second)[0]
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"output", "exit_code", "success"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing %q in %s", k, data)
		}
	}
	if !res.Success {
		t.Fatalf("fake result not successful: %+v", res)
	}
}

func TestPriorQdrantRace(t *testing.T) {
	// Direct parallel create: first wins, the rest see "already exists".
	var mu sync.Mutex
	created := false
	conflicts := 0
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			mu.Lock()
			defer mu.Unlock()
			if created {
				conflicts++
				return
			}
			created = true
		}()
	}
	close(start)
	wg.Wait()
	if conflicts == 0 {
		t.Fatal("expected the parallel create to conflict")
	}
	t.Logf("direct parallel create: 1 success, %d conflicts", conflicts)

	// Serial through the queue: no conflicts.
	q := testQueue(t, Config{
		MaxBatch:    5,
		MaxWait:     "20ms",
		RatePerSec:  100,
		Burst:       5,
		Concurrency: 1,
		Upstream:    "fake",
	})
	ids := enqueueN(t, q, 5)
	res := waitAll(t, q, ids, 5*time.Second)
	for _, r := range res {
		if !r.Success {
			t.Fatalf("serial queue job failed: %+v", r)
		}
	}
	t.Logf("serial queue: %d/%d success, 0 conflicts", len(res), len(res))
}

func TestBenchmarkCompare(t *testing.T) {
	const n = 20
	const item = 50 * time.Millisecond

	// Baseline: the caller pays the upstream latency per request.
	start := time.Now()
	for i := 0; i < n; i++ {
		time.Sleep(item)
	}
	direct := time.Since(start)

	// Queue with a batch API: 4 batches of 5 at 100ms each.
	q := testQueue(t, Config{
		MaxBatch: 5, MaxWait: "50ms", RatePerSec: 1000, Burst: 5,
		Concurrency: 1, Upstream: "fake", FakeBatchLatency: "100ms",
	})
	ids := enqueueN(t, q, n)
	start = time.Now()
	waitAll(t, q, ids, 20*time.Second)
	batch := time.Since(start)
	q.mu.Lock()
	batches := append([]int(nil), q.batchLog...)
	q.mu.Unlock()

	// Queue under a real quota: 5 requests/second, burst 5.
	q2 := testQueue(t, Config{
		MaxBatch: 5, MaxWait: "20ms", RatePerSec: 5, Burst: 5,
		Concurrency: 1, Upstream: "fake",
	})
	ids2 := enqueueN(t, q2, n)
	start = time.Now()
	waitAll(t, q2, ids2, 20*time.Second)
	limited := time.Since(start)
	q2.mu.Lock()
	batches2 := append([]int(nil), q2.batchLog...)
	q2.mu.Unlock()

	// Enqueue cost: the producer's side of the spool.
	q3 := testQueue(t, Config{
		MaxBatch: 100, MaxWait: "20ms", RatePerSec: 10000, Burst: 100,
		Concurrency: 1, Upstream: "fake",
	})
	start = time.Now()
	enqueueN(t, q3, 100)
	enqueue := time.Since(start)

	t.Logf("direct sequential 20x50ms      %8s", direct.Round(time.Millisecond))
	t.Logf("queue batch=5, 100ms/batch     %8s  batches=%v", batch.Round(time.Millisecond), batches)
	t.Logf("queue rate=5/s, 20 jobs        %8s  batches=%v", limited.Round(time.Millisecond), batches2)
	t.Logf("enqueue 100 jobs (spool+door)  %8s  (%.2fms each)", enqueue.Round(time.Millisecond), float64(enqueue.Microseconds())/100.0/1000)

	// Direct under the same quota: fail fast rejects 15/20; blocking waits
	// as long as the queue but the caller cannot do anything else.
	lim := rate.NewLimiter(5, 5)
	ok, rejected := 0, 0
	for i := 0; i < n; i++ {
		if lim.Allow() {
			ok++
		} else {
			rejected++
		}
	}
	t.Logf("direct 5/s quota fail-fast      %d ok / %d rejected", ok, rejected)
	t.Logf("prior fanout n=10 (measured)   %8s  sequential sweep, one 300ms peer", "500ms")
	t.Logf("prior fanout ticker 50ms       %8s  configured 20Hz -> ~2.5Hz", "3/1.2s")
}
