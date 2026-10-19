package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeFetcher struct {
	mu    sync.Mutex
	count int
	delay time.Duration
	fail  bool
	body  []byte
}

func (f *fakeFetcher) Fetch(ctx context.Context, _ string, _ int, _ string) ([]byte, error) {
	f.mu.Lock()
	f.count++
	delay := f.delay
	fail := f.fail
	body := append([]byte(nil), f.body...)
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail {
		return nil, errors.New("fetch failed")
	}
	return body, nil
}

func (f *fakeFetcher) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

func (f *fakeFetcher) SetFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}

func testCache(t *testing.T, cfg Config, f Fetcher) *Cache {
	t.Helper()
	if cfg.Name == "" {
		cfg.Name = "test"
	}
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	if cfg.SQLite == "" {
		cfg.SQLite = filepath.Join(cfg.Dir, "cache.db")
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if cfg.TTL == "" {
		cfg.TTL = "1m"
	}
	if cfg.MaxStale == "" {
		cfg.MaxStale = "24h"
	}
	c, err := New(cfg, f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestFreshHitDoesNotFetch(t *testing.T) {
	f := &fakeFetcher{body: []byte("v1")}
	c := testCache(t, Config{TTL: "1m"}, f)
	ctx := context.Background()
	e1, err := c.Get(ctx, "o/r", 1, "view", false)
	if err != nil {
		t.Fatal(err)
	}
	if e1.Source != "gh" || string(e1.Body) != "v1" {
		t.Fatalf("first: %+v", e1)
	}
	e2, err := c.Get(ctx, "o/r", 1, "view", false)
	if err != nil {
		t.Fatal(err)
	}
	if e2.Source != "cache" {
		t.Fatalf("second source = %q", e2.Source)
	}
	if f.Count() != 1 {
		t.Fatalf("fetcher called %d times", f.Count())
	}
}

func TestExpiryFetchesAgain(t *testing.T) {
	f := &fakeFetcher{body: []byte("v1")}
	c := testCache(t, Config{TTL: "20ms"}, f)
	ctx := context.Background()
	if _, err := c.Get(ctx, "o/r", 1, "view", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := c.Get(ctx, "o/r", 1, "view", false); err != nil {
		t.Fatal(err)
	}
	if f.Count() != 2 {
		t.Fatalf("fetcher called %d times", f.Count())
	}
}

func TestSingleFlight(t *testing.T) {
	f := &fakeFetcher{body: []byte("v1"), delay: 50 * time.Millisecond}
	c := testCache(t, Config{TTL: "1m"}, f)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := c.Get(ctx, "o/r", 1, "view", false)
			if err != nil {
				errs <- err
				return
			}
			if string(e.Body) != "v1" {
				errs <- fmt.Errorf("body = %q", e.Body)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if f.Count() != 1 {
		t.Fatalf("single-flight failed: %d fetches", f.Count())
	}
}

func TestStaleOnError(t *testing.T) {
	f := &fakeFetcher{body: []byte("v1")}
	c := testCache(t, Config{TTL: "20ms", MaxStale: "1h"}, f)
	ctx := context.Background()
	if _, err := c.Get(ctx, "o/r", 1, "view", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	f.SetFail(true)
	e, err := c.Get(ctx, "o/r", 1, "view", false)
	if err != nil {
		t.Fatalf("expected stale fallback, got %v", err)
	}
	if !e.Stale || e.Source != "stale" || string(e.Body) != "v1" {
		t.Fatalf("stale entry: %+v", e)
	}
}

func TestRefreshBypassesFreshRow(t *testing.T) {
	f := &fakeFetcher{body: []byte("v1")}
	c := testCache(t, Config{TTL: "1m"}, f)
	ctx := context.Background()
	if _, err := c.Get(ctx, "o/r", 1, "view", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "o/r", 1, "view", true); err != nil {
		t.Fatal(err)
	}
	if f.Count() != 2 {
		t.Fatalf("refresh did not fetch: %d", f.Count())
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	f := &fakeFetcher{body: []byte("v1")}
	c1, err := New(Config{Name: "persist", Dir: dir, SQLite: filepath.Join(dir, "cache.db"), Listen: "127.0.0.1:0", TTL: "1m"}, f)
	if err != nil {
		t.Fatal(err)
	}
	if _, getErr := c1.Get(context.Background(), "o/r", 1, "view", false); getErr != nil {
		t.Fatal(getErr)
	}
	if closeErr := c1.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	c2, err := New(Config{Name: "persist", Dir: dir, SQLite: filepath.Join(dir, "cache.db"), Listen: "127.0.0.1:0", TTL: "1m"}, f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.Close() }()
	e, err := c2.Get(context.Background(), "o/r", 1, "view", false)
	if err != nil {
		t.Fatal(err)
	}
	if e.Source != "cache" || f.Count() != 1 {
		t.Fatalf("reopen did not use the row: source=%q fetches=%d", e.Source, f.Count())
	}
}

func TestRepoAllowList(t *testing.T) {
	f := &fakeFetcher{body: []byte("v1")}
	c := testCache(t, Config{TTL: "1m", RepoAllow: []string{"o/r"}}, f)
	if _, err := c.Get(context.Background(), "other/repo", 1, "view", false); err == nil {
		t.Fatal("expected allow-list refusal")
	}
}

func TestHTTPHit(t *testing.T) {
	f := &fakeFetcher{body: []byte(`{"number":1}`)}
	c := testCache(t, Config{TTL: "1m"}, f)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	get := func() *http.Response {
		t.Helper()
		resp, err := http.Get(c.URL() + "/pr?repo=o/r&number=1&kind=view") //nolint:noctx // test
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	r1 := get()
	body1, _ := io.ReadAll(r1.Body)
	_ = r1.Body.Close()
	if r1.Header.Get("X-Cache") != "gh" || string(body1) != `{"number":1}` {
		t.Fatalf("first: cache=%q body=%q", r1.Header.Get("X-Cache"), body1)
	}
	r2 := get()
	_, _ = io.ReadAll(r2.Body)
	_ = r2.Body.Close()
	if r2.Header.Get("X-Cache") != "cache" {
		t.Fatalf("second: cache=%q", r2.Header.Get("X-Cache"))
	}
}
