// Package queue is a lezz-owned sidecar for expensive or rate-limited
// upstreams. It is a spool directory plus a FIFO doorbell:
//
//	jobs/<id>.json     the queue of record (atomic writes, survives restart)
//	doorbell           one byte per enqueue (wakeup + backpressure only)
//	results/<id>.json  completion record (the "update when it's done")
//	dlq/               failed jobs
//
// A worker drains up to MaxBatch jobs at a time, under a token bucket, and
// executes them through an Upstream. Batching selects jobs; it does not imply
// parallel execution — serial upstreams set Concurrency=1.
package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
)

// Config is the static definition lezz owns.
type Config struct {
	Name         string   `yaml:"name"`
	Dir          string   `yaml:"dir"`
	Listen       string   `yaml:"listen"`
	MaxBatch     int      `yaml:"max_batch"`
	MaxWait      string   `yaml:"max_wait"`
	RatePerSec   float64  `yaml:"rate_per_sec"`
	Burst        int      `yaml:"burst"`
	Concurrency  int      `yaml:"concurrency"`
	MaxDepth     int      `yaml:"max_depth"`
	Fsync        bool     `yaml:"fsync"` // durability against power loss; rename is enough for process crashes
	SQLiteMirror string   `yaml:"sqlite_mirror"`
	Upstream     string   `yaml:"upstream"` // gh | fake
	Command      string   `yaml:"command"`
	ArgsPrefix   []string `yaml:"args_prefix,omitempty"`
	// Fake upstream knobs (used by tests and the benchmark).
	FakeLatency      string `yaml:"fake_latency,omitempty"`
	FakeBatchLatency string `yaml:"fake_batch_latency,omitempty"`
}

// Job is one unit of work.
type Job struct {
	ID             string    `json:"id"`
	Args           []string  `json:"args"`
	TimeoutSeconds int       `json:"timeout_seconds,omitempty"`
	Priority       int       `json:"priority,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Result is the completion record, shaped like adhd.gh.run's response.
type Result struct {
	ID         string    `json:"id"`
	Output     string    `json:"output"`
	ExitCode   int       `json:"exit_code"`
	Success    bool      `json:"success"`
	Error      string    `json:"error,omitempty"`
	Version    string    `json:"version,omitempty"`
	QueuedAt   time.Time `json:"queued_at"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	BatchSize  int       `json:"batch_size"`
	WaitMS     int64     `json:"wait_ms"`
}

// ErrFull is returned when the spool is at MaxDepth.
var ErrFull = errors.New("queue is full")

// Upstream executes a selected batch. Implementations may run the jobs
// serially or concurrently; batching only chooses what to hand over.
type Upstream interface {
	Execute(ctx context.Context, jobs []*Job) map[string]*Result
}

// Queue is a running spool + worker + HTTP surface.
type Queue struct {
	cfg   Config
	dir   string
	door  string
	store Store
	torn  []string

	limiter *rate.Limiter
	up      Upstream
	srv     *http.Server
	ln      net.Listener
	baseURL string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	completed atomic.Int64
	failed    atomic.Int64
	batches   atomic.Int64
	inFlight  atomic.Int64
	dropped   atomic.Int64

	mu       sync.Mutex
	batchLog []int
	lastWait time.Duration
}

// New validates and materializes a queue.
func New(cfg Config) (*Queue, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	file, err := newFileStore(cfg.Dir, cfg.Fsync)
	if err != nil {
		return nil, err
	}
	store := Store(file)
	if cfg.SQLiteMirror != "" {
		sqlite, sqErr := newSQLiteStore(cfg.SQLiteMirror)
		if sqErr != nil {
			_ = file.Close()
			return nil, sqErr
		}
		store = newMirrorStore(file, sqlite)
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		cfg:    cfg,
		dir:    cfg.Dir,
		door:   filepath.Join(cfg.Dir, "doorbell"),
		store:  store,
		ctx:    ctx,
		cancel: cancel,
	}
	if cfg.Burst < cfg.MaxBatch {
		cfg.Burst = cfg.MaxBatch
		q.cfg.Burst = cfg.Burst
	}
	if cfg.RatePerSec > 0 {
		q.limiter = rate.NewLimiter(rate.Limit(cfg.RatePerSec), cfg.Burst)
	}
	switch cfg.Upstream {
	case "fake":
		item, _ := time.ParseDuration(cfg.FakeLatency)
		batch, _ := time.ParseDuration(cfg.FakeBatchLatency)
		q.up = &fakeUpstream{latency: item, batchLatency: batch, concurrency: cfg.Concurrency}
	case "gh", "cmd":
		q.up = &execUpstream{command: cfg.Command, prefix: cfg.ArgsPrefix, concurrency: cfg.Concurrency}
	default:
		return nil, fmt.Errorf("queue %s: unknown upstream %q", cfg.Name, cfg.Upstream)
	}
	return q, nil
}

// Start creates the doorbell FIFO, starts the worker, and serves HTTP.
func (q *Queue) Start(parent context.Context) error {
	if err := syscall.Mkfifo(q.door, 0o600); err != nil && !errors.Is(err, syscall.EEXIST) {
		return fmt.Errorf("queue %s: mkfifo: %w", q.cfg.Name, err)
	}
	if m, ok := q.store.(*mirrorStore); ok {
		torn, err := m.Reconcile(parent)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: queue %s mirror reconcile: %v\n", q.cfg.Name, err)
		}
		q.torn = torn
		if len(torn) > 0 {
			fmt.Fprintf(os.Stderr, "warning: queue %s mirror torn: %v\n", q.cfg.Name, torn)
		}
	}
	ln, err := net.Listen("tcp", q.cfg.Listen)
	if err != nil {
		return fmt.Errorf("queue %s: listen: %w", q.cfg.Name, err)
	}
	q.ln = ln
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		q.baseURL = fmt.Sprintf("http://127.0.0.1:%d", addr.Port)
	} else {
		q.baseURL = ln.Addr().String()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/enqueue", q.handleEnqueue)
	mux.HandleFunc("/result/", q.handleResult)
	mux.HandleFunc("/status", q.handleStatus)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	q.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = q.srv.Serve(ln) }()

	q.wg.Add(1)
	go q.work()
	go func() {
		select {
		case <-parent.Done():
			_ = q.Close()
		case <-q.ctx.Done():
		}
	}()
	return nil
}

// URL is the loopback HTTP endpoint.
func (q *Queue) URL() string { return q.baseURL }

// Dir is the spool directory.
func (q *Queue) Dir() string { return q.dir }

// StoreName identifies the active driver ("file" or "file+sqlite").
func (q *Queue) StoreName() string {
	if q.store == nil {
		return ""
	}
	return q.store.Name()
}

// Enqueue writes a job to the spool and rings the doorbell.
func (q *Queue) Enqueue(job *Job) error {
	if job.ID == "" {
		job.ID = newID()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	if q.cfg.MaxDepth > 0 && q.depth() >= q.cfg.MaxDepth {
		q.dropped.Add(1)
		return ErrFull
	}
	if err := q.store.PutJob(context.Background(), job); err != nil {
		return err
	}
	q.ring()
	return nil
}

// Wait blocks until the job's result exists or ctx is done.
func (q *Queue) Wait(ctx context.Context, id string) (*Result, error) {
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()
	for {
		res, err := q.store.GetResult(ctx, id)
		if err == nil {
			return res, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Close stops the worker and HTTP listener. Jobs on disk are kept.
func (q *Queue) Close() error {
	q.cancel()
	if q.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = q.srv.Shutdown(ctx)
		cancel()
	}
	q.wg.Wait()
	if q.store != nil {
		_ = q.store.Close()
	}
	return nil
}

func (q *Queue) work() {
	defer q.wg.Done()
	// The FIFO read end is non-blocking; poll it for wakeups. A ticker is the
	// safety net (missed doorbell, jobs written by an operator).
	f, err := os.OpenFile(q.door, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		f = nil
	}
	if f != nil {
		defer func() { _ = f.Close() }()
	}
	tick := time.NewTicker(q.cfg.maxWait() / 2)
	defer tick.Stop()
	buf := make([]byte, 64)
	for {
		q.drain()
		if f == nil {
			select {
			case <-q.ctx.Done():
				return
			case <-tick.C:
			}
			continue
		}
		// unix.PollFd.Fd is an int32, while File.Fd is a uintptr. A descriptor
		// that does not fit would be silently truncated and poll a different
		// descriptor, so bounds-check it and fall back to the ticker instead.
		fd := f.Fd()
		if fd > math.MaxInt32 {
			select {
			case <-q.ctx.Done():
				return
			case <-tick.C:
			}
			continue
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, _ := unix.Poll(fds, 100)
		if n > 0 {
			for {
				if _, readErr := f.Read(buf); readErr != nil {
					break
				}
			}
		}
		select {
		case <-q.ctx.Done():
			return
		case <-tick.C:
		default:
		}
	}
}

func (q *Queue) drain() {
	jobs, err := q.store.ListJobs(q.ctx)
	if err != nil || len(jobs) == 0 {
		return
	}
	// Linger briefly so a burst becomes one batch (the "queue five" window)
	// instead of five one-job batches. Drain immediately when the batch is full.
	if len(jobs) < q.cfg.MaxBatch {
		deadline := time.Now().Add(q.cfg.maxWait())
		for len(jobs) < q.cfg.MaxBatch && time.Now().Before(deadline) {
			select {
			case <-q.ctx.Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
			jobs, err = q.store.ListJobs(q.ctx)
			if err != nil {
				return
			}
			if len(jobs) == 0 {
				break
			}
		}
	}
	if len(jobs) == 0 {
		return
	}
	if len(jobs) > q.cfg.MaxBatch {
		jobs = jobs[:q.cfg.MaxBatch]
	}
	if q.limiter != nil {
		waitStart := time.Now()
		if err := q.limiter.WaitN(q.ctx, len(jobs)); err != nil {
			return
		}
		q.mu.Lock()
		q.lastWait = time.Since(waitStart)
		q.mu.Unlock()
	}
	q.batches.Add(1)
	q.inFlight.Add(int64(len(jobs)))
	q.mu.Lock()
	q.batchLog = append(q.batchLog, len(jobs))
	if len(q.batchLog) > 100 {
		q.batchLog = q.batchLog[len(q.batchLog)-100:]
	}
	q.mu.Unlock()

	results := q.up.Execute(q.ctx, jobs)
	for _, job := range jobs {
		res := results[job.ID]
		if res == nil {
			res = &Result{ID: job.ID, ExitCode: -1, Error: "upstream returned no result"}
		}
		res.QueuedAt = job.CreatedAt
		if res.FinishedAt.IsZero() {
			res.FinishedAt = time.Now().UTC()
		}
		res.WaitMS = res.StartedAt.Sub(job.CreatedAt).Milliseconds()
		res.BatchSize = len(jobs)
		if res.Error != "" || (!res.Success && res.ExitCode != 0) {
			q.failed.Add(1)
		} else {
			q.completed.Add(1)
		}
		if err := q.store.PutResult(q.ctx, res); err != nil {
			fmt.Fprintf(os.Stderr, "warning: queue %s result %s: %v\n", q.cfg.Name, job.ID, err)
			continue
		}
		if err := q.store.DeleteJob(q.ctx, job.ID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: queue %s delete job %s: %v\n", q.cfg.Name, job.ID, err)
		}
		if err := q.store.AppendCommit(q.ctx, job, res); err != nil {
			fmt.Fprintf(os.Stderr, "warning: queue %s journal %s: %v\n", q.cfg.Name, job.ID, err)
		}
	}
	q.inFlight.Add(-int64(len(jobs)))
}

func (q *Queue) depth() int {
	jobs, err := q.store.ListJobs(context.Background())
	if err != nil {
		return 0
	}
	return len(jobs)
}

func (q *Queue) oldestAge() time.Duration {
	jobs, err := q.store.ListJobs(context.Background())
	if err != nil || len(jobs) == 0 {
		return 0
	}
	return time.Since(jobs[0].CreatedAt)
}

func (q *Queue) ring() {
	f, err := os.OpenFile(q.door, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return // no reader yet; the ticker will pick it up
	}
	_, _ = f.Write([]byte{1})
	_ = f.Close()
}

// ---- HTTP -----------------------------------------------------------------

type enqueueRequest struct {
	Args           []string `json:"args"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	Priority       int      `json:"priority,omitempty"`
	Wait           bool     `json:"wait,omitempty"`
}

func (q *Queue) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()
	var req enqueueRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	job := &Job{Args: req.Args, TimeoutSeconds: req.TimeoutSeconds, Priority: req.Priority}
	if err := q.Enqueue(job); err != nil {
		if errors.Is(err, ErrFull) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "queue is full", http.StatusTooManyRequests)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !req.Wait {
		writeJSON(w, http.StatusAccepted, map[string]any{"id": job.ID, "status": "queued"})
		return
	}
	timeout := 120 * time.Second
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	res, err := q.Wait(ctx, job.ID)
	if err != nil {
		http.Error(w, "wait: "+err.Error(), http.StatusGatewayTimeout)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (q *Queue) handleResult(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/result/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	res, err := q.store.GetResult(r.Context(), id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (q *Queue) handleStatus(w http.ResponseWriter, _ *http.Request) {
	q.mu.Lock()
	batches := append([]int(nil), q.batchLog...)
	lastWait := q.lastWait
	q.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"name":              q.cfg.Name,
		"url":               q.baseURL,
		"spool":             q.dir,
		"store":             q.store.Name(),
		"torn":              len(q.torn),
		"depth":             q.depth(),
		"oldest_age_ms":     q.oldestAge().Milliseconds(),
		"in_flight":         q.inFlight.Load(),
		"completed":         q.completed.Load(),
		"failed":            q.failed.Load(),
		"batches":           q.batches.Load(),
		"batch_sizes":       batches,
		"last_rate_wait_ms": lastWait.Milliseconds(),
		"max_batch":         q.cfg.MaxBatch,
		"max_wait_ms":       q.cfg.maxWait().Milliseconds(),
		"rate_per_sec":      q.cfg.RatePerSec,
		"max_depth":         q.cfg.MaxDepth,
		"dropped":           q.dropped.Load(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- upstreams ------------------------------------------------------------

type fakeUpstream struct {
	latency      time.Duration
	batchLatency time.Duration
	concurrency  int
}

func (f *fakeUpstream) Execute(ctx context.Context, jobs []*Job) map[string]*Result {
	out := make(map[string]*Result, len(jobs))
	if f.batchLatency > 0 {
		sleepCtx(ctx, f.batchLatency)
	}
	sem := make(chan struct{}, maxInt(1, f.concurrency))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, job := range jobs {
		wg.Add(1)
		go func(j *Job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now().UTC()
			sleepCtx(ctx, f.latency)
			mu.Lock()
			out[j.ID] = &Result{
				ID:         j.ID,
				Output:     "ok " + j.ID,
				Success:    true,
				StartedAt:  start,
				FinishedAt: time.Now().UTC(),
			}
			mu.Unlock()
		}(job)
	}
	wg.Wait()
	return out
}

type execUpstream struct {
	command     string
	prefix      []string
	concurrency int
}

func (e *execUpstream) Execute(ctx context.Context, jobs []*Job) map[string]*Result {
	out := make(map[string]*Result, len(jobs))
	sem := make(chan struct{}, maxInt(1, e.concurrency))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, job := range jobs {
		wg.Add(1)
		go func(j *Job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res := &Result{ID: j.ID, StartedAt: time.Now().UTC()}
			timeout := 30 * time.Second
			if j.TimeoutSeconds > 0 {
				timeout = time.Duration(j.TimeoutSeconds) * time.Second
			}
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			args := append(append([]string{}, e.prefix...), j.Args...)
			cmd := exec.CommandContext(runCtx, e.command, args...) //nolint:gosec // command is from lezz config; args are the job payload
			outBytes, err := cmd.CombinedOutput()
			res.Output = string(outBytes)
			res.FinishedAt = time.Now().UTC()
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					res.ExitCode = exitErr.ExitCode()
					res.Error = "exit " + strconv.Itoa(res.ExitCode)
				} else {
					res.ExitCode = -1
					res.Error = err.Error()
				}
				res.Success = false
			} else {
				res.Success = true
			}
			mu.Lock()
			out[j.ID] = res
			mu.Unlock()
		}(job)
	}
	wg.Wait()
	return out
}

// ---- config ---------------------------------------------------------------

func (c *Config) normalize() error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return errors.New("queue: name is required")
	}
	if c.Dir == "" {
		home, _ := os.UserHomeDir()
		c.Dir = filepath.Join(home, ".lezz", "queues", c.Name)
	}
	c.Dir = expandHome(c.Dir)
	c.SQLiteMirror = expandHome(c.SQLiteMirror)
	if c.Listen == "" {
		c.Listen = "127.0.0.1:0"
	}
	if c.MaxBatch <= 0 {
		c.MaxBatch = 1
	}
	if c.Burst < c.MaxBatch {
		c.Burst = c.MaxBatch
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 1
	}
	if c.MaxWait == "" {
		c.MaxWait = "200ms"
	}
	if _, err := time.ParseDuration(c.MaxWait); err != nil {
		return fmt.Errorf("queue %s: max_wait: %w", c.Name, err)
	}
	if c.Upstream == "" {
		c.Upstream = "fake"
	}
	if (c.Upstream == "gh" || c.Upstream == "cmd") && c.Command == "" {
		c.Command = "gh"
	}
	if c.FakeLatency != "" {
		if _, err := time.ParseDuration(c.FakeLatency); err != nil {
			return fmt.Errorf("queue %s: fake_latency: %w", c.Name, err)
		}
	}
	if c.FakeBatchLatency != "" {
		if _, err := time.ParseDuration(c.FakeBatchLatency); err != nil {
			return fmt.Errorf("queue %s: fake_batch_latency: %w", c.Name, err)
		}
	}
	return nil
}

func (c Config) maxWait() time.Duration {
	d, _ := time.ParseDuration(c.MaxWait)
	if d <= 0 {
		d = 200 * time.Millisecond
	}
	return d
}

// LoadConfig reads and parses a queue configuration from the YAML file at path.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is a user-configured queue state file.
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

// ---- helpers --------------------------------------------------------------

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

func writeJSONAtomic(path string, v any, fsync bool) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if fsync {
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			_ = os.Remove(name)
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func readResult(path string) (*Result, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is a user-configured queue state file.
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
