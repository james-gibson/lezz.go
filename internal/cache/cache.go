// Package cache is a lezz-owned read-through cache for GitHub pull requests.
// It is the read-side counterpart to the queue: repeated reads of the same PR
// are served from SQLite, and concurrent misses are single-flighted so five
// agents cause one `gh` call, not five.
package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

// Config is the static definition lezz owns.
type Config struct {
	Name         string   `yaml:"name"`
	Dir          string   `yaml:"dir"`
	SQLite       string   `yaml:"sqlite"`
	Listen       string   `yaml:"listen"`
	TTL          string   `yaml:"ttl"`
	MaxStale     string   `yaml:"max_stale"`
	Command      string   `yaml:"command"`
	RepoAllow    []string `yaml:"repo_allow"`
	MaxBodyBytes int64    `yaml:"max_body_bytes"`

	ttl      time.Duration
	maxStale time.Duration
}

// Entry is one cached PR artifact. Body is the raw `gh` output.
type Entry struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Kind      string    `json:"kind"`
	Body      []byte    `json:"-"`
	FetchedAt time.Time `json:"fetched_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Source    string    `json:"source"` // cache | gh | stale
	Stale     bool      `json:"stale"`
}

// Fetcher is the only thing that talks to GitHub. Tests inject a fake.
type Fetcher interface {
	Fetch(ctx context.Context, repo string, number int, kind string) ([]byte, error)
}

// Cache is a SQLite-backed read-through cache with single-flight misses.
type Cache struct {
	cfg Config
	db  *sql.DB
	f   Fetcher
	srv *http.Server
	ln  net.Listener

	closeOnce sync.Once
	closeErr  error

	mu       sync.Mutex
	inflight map[string]*flight

	hits    atomic.Int64
	misses  atomic.Int64
	stale   atomic.Int64
	fetches atomic.Int64
}

type flight struct {
	done  chan struct{}
	entry *Entry
	err   error
}

const schema = `
CREATE TABLE IF NOT EXISTS pr_cache (
  key        TEXT PRIMARY KEY,
  repo       TEXT NOT NULL,
  number     INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  body       BLOB NOT NULL,
  fetched_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
`

// New opens the cache. A nil fetcher uses the gh CLI.
func New(cfg Config, fetcher Fetcher) (*Cache, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("cache %s: mkdir: %w", cfg.Name, err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SQLite), 0o700); err != nil {
		return nil, fmt.Errorf("cache %s: sqlite dir: %w", cfg.Name, err)
	}
	db, err := sql.Open("sqlite", cfg.SQLite)
	if err != nil {
		return nil, fmt.Errorf("cache %s: open sqlite: %w", cfg.Name, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("cache %s: %s: %w", cfg.Name, pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cache %s: schema: %w", cfg.Name, err)
	}
	if fetcher == nil {
		fetcher = &GHFetcher{command: cfg.Command, maxBody: cfg.MaxBodyBytes}
	}
	return &Cache{cfg: cfg, db: db, f: fetcher, inflight: make(map[string]*flight)}, nil
}

// Start serves the read API on loopback.
func (c *Cache) Start(parent context.Context) error {
	ln, err := net.Listen("tcp", c.cfg.Listen)
	if err != nil {
		return fmt.Errorf("cache %s: listen: %w", c.cfg.Name, err)
	}
	c.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/pr", c.handlePR)
	mux.HandleFunc("/status", c.handleStatus)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	c.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = c.srv.Serve(ln) }()
	go func() {
		<-parent.Done()
		_ = c.Close()
	}()
	return nil
}

// URL is the loopback base URL.
func (c *Cache) URL() string {
	if c.ln == nil {
		return ""
	}
	addr, ok := c.ln.Addr().(*net.TCPAddr)
	if !ok {
		return "http://" + c.ln.Addr().String()
	}
	return fmt.Sprintf("http://127.0.0.1:%d", addr.Port)
}

// DB is the SQLite path.
func (c *Cache) DB() string { return c.cfg.SQLite }

// TTL is the configured freshness window.
func (c *Cache) TTL() time.Duration { return c.cfg.ttl }

// Close stops the listener and closes the database.
func (c *Cache) Close() error {
	c.closeOnce.Do(func() {
		if c.srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = c.srv.Shutdown(ctx)
			cancel()
		}
		if c.db != nil {
			c.closeErr = c.db.Close()
		}
	})
	return c.closeErr
}

// Get returns a fresh entry, fetches one, or falls back to stale within
// MaxStale. Concurrent misses for the same key share one fetch.
func (c *Cache) Get(ctx context.Context, repo string, number int, kind string, refresh bool) (*Entry, error) {
	if err := c.validate(repo, number, kind); err != nil {
		return nil, err
	}
	key := cacheKey(repo, number, kind)
	if !refresh {
		if e, err := c.getRow(ctx, key); err == nil && time.Now().Before(e.ExpiresAt) {
			c.hits.Add(1)
			e.Source = "cache"
			return e, nil
		}
	}
	c.misses.Add(1)
	return c.singleFlight(ctx, key, repo, number, kind, refresh)
}

func (c *Cache) singleFlight(ctx context.Context, key, repo string, number int, kind string, refresh bool) (*Entry, error) {
	c.mu.Lock()
	if f, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.entry, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	c.inflight[key] = f
	c.mu.Unlock()

	entry, err := c.fetch(ctx, key, repo, number, kind, refresh)
	f.entry, f.err = entry, err
	close(f.done)
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
	return entry, err
}

func (c *Cache) fetch(ctx context.Context, key, repo string, number int, kind string, refresh bool) (*Entry, error) {
	// Another flight may have populated the row between the fast-path miss and
	// becoming the leader.
	if !refresh {
		if e, err := c.getRow(ctx, key); err == nil && time.Now().Before(e.ExpiresAt) {
			c.hits.Add(1)
			e.Source = "cache"
			return e, nil
		}
	}
	c.fetches.Add(1)
	body, err := c.f.Fetch(ctx, repo, number, kind)
	if err != nil {
		if e, rerr := c.getRow(ctx, key); rerr == nil {
			if c.cfg.maxStale <= 0 || time.Since(e.FetchedAt) <= c.cfg.maxStale {
				c.stale.Add(1)
				e.Source = "stale"
				e.Stale = true
				return e, nil
			}
		}
		return nil, err
	}
	now := time.Now().UTC()
	e := &Entry{
		Repo:      repo,
		Number:    number,
		Kind:      kind,
		Body:      body,
		FetchedAt: now,
		ExpiresAt: now.Add(c.cfg.ttl),
		Source:    "gh",
	}
	if err := c.putRow(ctx, key, e); err != nil {
		return nil, err
	}
	return e, nil
}

func (c *Cache) validate(repo string, number int, kind string) error {
	if strings.TrimSpace(repo) == "" {
		return errors.New("repo is required (owner/name)")
	}
	if number <= 0 {
		return errors.New("number must be positive")
	}
	if kind != "view" && kind != "diff" {
		return fmt.Errorf("kind must be view or diff, got %q", kind)
	}
	if len(c.cfg.RepoAllow) > 0 {
		for _, allowed := range c.cfg.RepoAllow {
			if allowed == repo {
				return nil
			}
		}
		return fmt.Errorf("repo %q is not in repo_allow", repo)
	}
	return nil
}

func (c *Cache) getRow(ctx context.Context, key string) (*Entry, error) {
	var (
		e                  Entry
		fetchedAt, expires string
	)
	err := c.db.QueryRowContext(ctx,
		`SELECT repo, number, kind, body, fetched_at, expires_at FROM pr_cache WHERE key=?`, key).
		Scan(&e.Repo, &e.Number, &e.Kind, &e.Body, &fetchedAt, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	e.FetchedAt = parseTime(fetchedAt)
	e.ExpiresAt = parseTime(expires)
	return &e, nil
}

func (c *Cache) putRow(ctx context.Context, key string, e *Entry) error {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO pr_cache(key, repo, number, kind, body, fetched_at, expires_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   repo=excluded.repo, number=excluded.number, kind=excluded.kind,
		   body=excluded.body, fetched_at=excluded.fetched_at, expires_at=excluded.expires_at`,
		key, e.Repo, e.Number, e.Kind, e.Body, formatTime(e.FetchedAt), formatTime(e.ExpiresAt))
	return err
}

func (c *Cache) handlePR(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo := q.Get("repo")
	number, _ := strconv.Atoi(q.Get("number"))
	kind := q.Get("kind")
	if kind == "" {
		kind = "view"
	}
	refresh := q.Get("refresh") == "1" || q.Get("refresh") == "true"
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	e, err := c.Get(ctx, repo, number, kind, refresh)
	if err != nil {
		code := http.StatusBadGateway
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "must be") || strings.Contains(err.Error(), "not in repo_allow") {
			code = http.StatusBadRequest
		}
		http.Error(w, err.Error(), code)
		return
	}
	if kind == "diff" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("X-Cache", e.Source)
	w.Header().Set("X-Fetched-At", e.FetchedAt.Format(time.RFC3339))
	if e.Stale {
		w.Header().Set("X-Stale", "true")
	}
	_, _ = w.Write(e.Body)
}

func (c *Cache) handleStatus(w http.ResponseWriter, _ *http.Request) {
	var entries int
	_ = c.db.QueryRow(`SELECT COUNT(*) FROM pr_cache`).Scan(&entries)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":         c.cfg.Name,
		"url":          c.URL(),
		"db":           c.cfg.SQLite,
		"ttl_ms":       c.cfg.ttl.Milliseconds(),
		"max_stale_ms": c.cfg.maxStale.Milliseconds(),
		"hits":         c.hits.Load(),
		"misses":       c.misses.Load(),
		"stale":        c.stale.Load(),
		"fetches":      c.fetches.Load(),
		"entries":      entries,
	})
}

// ---- gh fetcher -----------------------------------------------------------

// GHFetcher shells out to the authenticated gh CLI.
type GHFetcher struct {
	command string
	maxBody int64
}

// Fetch runs `gh` for one PR artifact and returns its raw output.
func (g *GHFetcher) Fetch(ctx context.Context, repo string, number int, kind string) ([]byte, error) {
	n := strconv.Itoa(number)
	var args []string
	switch kind {
	case "view":
		args = []string{
			"pr", "view", n, "-R", repo, "--json",
			"number,title,state,url,author,body,createdAt,updatedAt,baseRefName,headRefName,isDraft,mergeable,additions,deletions,changedFiles,files,comments,reviewDecision",
		}
	case "diff":
		args = []string{"pr", "diff", n, "-R", repo}
	default:
		return nil, fmt.Errorf("unsupported kind %q", kind)
	}
	cmd := exec.CommandContext(ctx, g.command, args...) //nolint:gosec // command is from lezz config; repo/number are validated
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("gh: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	if g.maxBody > 0 && int64(len(out)) > g.maxBody {
		return nil, fmt.Errorf("response exceeds max_body_bytes (%d)", g.maxBody)
	}
	return out, nil
}

// ---- config ---------------------------------------------------------------

func (c *Config) normalize() error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return errors.New("cache: name is required")
	}
	if c.Dir == "" {
		home, _ := os.UserHomeDir()
		c.Dir = filepath.Join(home, ".lezz", "cache", c.Name)
	}
	c.Dir = expandHome(c.Dir)
	if c.SQLite == "" {
		c.SQLite = filepath.Join(c.Dir, "cache.db")
	}
	c.SQLite = expandHome(c.SQLite)
	if c.Listen == "" {
		c.Listen = "127.0.0.1:0"
	}
	if c.TTL == "" {
		c.TTL = "60s"
	}
	if c.MaxStale == "" {
		c.MaxStale = "24h"
	}
	var err error
	c.ttl, err = time.ParseDuration(c.TTL)
	if err != nil {
		return fmt.Errorf("cache %s: ttl: %w", c.Name, err)
	}
	c.maxStale, err = time.ParseDuration(c.MaxStale)
	if err != nil {
		return fmt.Errorf("cache %s: max_stale: %w", c.Name, err)
	}
	if c.Command == "" {
		c.Command = "gh"
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 16 << 20
	}
	return nil
}

// LoadConfig reads a cache YAML.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the configured cache file.
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

func cacheKey(repo string, number int, kind string) string {
	return fmt.Sprintf("%s#%d#%s", repo, number, kind)
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
