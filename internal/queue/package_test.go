package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fileWriter is a single-writer upstream: it owns one package.json, applies
// each job as a semantic "add dependency" op, and bumps a version.
type fileWriter struct {
	mu      sync.Mutex
	path    string
	version int
}

func (w *fileWriter) Execute(_ context.Context, jobs []*Job) map[string]*Result {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]*Result, len(jobs))
	for _, j := range jobs {
		var doc map[string]any
		data, err := os.ReadFile(w.path)
		if err == nil {
			_ = json.Unmarshal(data, &doc)
		}
		if doc == nil {
			doc = map[string]any{}
		}
		deps, _ := doc["dependencies"].(map[string]any)
		if deps == nil {
			deps = map[string]any{}
			doc["dependencies"] = deps
		}
		name := j.Args[0]
		version := j.Args[1]
		deps[name] = version
		w.version++
		_ = writeJSONAtomic(w.path, doc, false)
		out[j.ID] = &Result{
			ID:         j.ID,
			Output:     fmt.Sprintf("applied %s=%s at version %d", name, version, w.version),
			Success:    true,
			StartedAt:  time.Now().UTC(),
			FinishedAt: time.Now().UTC(),
			BatchSize:  len(jobs),
		}
	}
	return out
}

func deps(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Dependencies map[string]any `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Dependencies
}

// TestPackageJSONFiveAgents compares direct read-modify-write with the queue's
// single writer. Five agents add five different dependencies.
func TestPackageJSONFiveAgents(t *testing.T) {
	agents := []struct{ name, version string }{
		{"react", "^18.3.1"},
		{"zod", "^3.23.8"},
		{"vitest", "^2.1.1"},
		{"eslint", "^9.9.0"},
		{"tsx", "^4.19.0"},
	}
	depNames := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		return out
	}

	// Direct: every agent reads, decides, then writes. Atomic writes do not
	// help — the last writer still wins.
	direct := filepath.Join(t.TempDir(), "package.json")
	if err := writeJSONAtomic(direct, map[string]any{"dependencies": map[string]any{}}, false); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, a := range agents {
		wg.Add(1)
		go func(name, version string) {
			defer wg.Done()
			<-start
			var doc map[string]any
			data, _ := os.ReadFile(direct)
			_ = json.Unmarshal(data, &doc)
			time.Sleep(15 * time.Millisecond) // widen the read-modify-write window
			deps, _ := doc["dependencies"].(map[string]any)
			if deps == nil {
				deps = map[string]any{}
				doc["dependencies"] = deps
			}
			deps[name] = version
			_ = writeJSONAtomic(direct, doc, false)
		}(a.name, a.version)
	}
	close(start)
	wg.Wait()
	directDeps := deps(t, direct)
	t.Logf("direct 5 agents: %d/%d dependencies survived: %v", len(directDeps), len(agents), depNames(directDeps))

	// Queue: five intents, one writer, one version per commit.
	serial := filepath.Join(t.TempDir(), "package.json")
	if err := writeJSONAtomic(serial, map[string]any{"dependencies": map[string]any{}}, false); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Name: "workspace", Dir: t.TempDir(), Listen: "127.0.0.1:0",
		MaxBatch: 5, MaxWait: "30ms", RatePerSec: 1000, Burst: 5,
		Concurrency: 1, Upstream: "fake",
	}
	q, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	q.up = &fileWriter{path: serial}
	if err := q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Close() }()

	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		j := &Job{Args: []string{a.name, a.version}}
		if err := q.Enqueue(j); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	versions := make([]string, 0, len(ids))
	for _, id := range ids {
		res, err := q.Wait(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, res.Output)
	}
	serialDeps := deps(t, serial)
	t.Logf("queue 5 agents: %d/%d dependencies survived: %v", len(serialDeps), len(agents), depNames(serialDeps))
	for _, v := range versions {
		t.Logf("  result: %s", v)
	}

	if len(serialDeps) != len(agents) {
		t.Fatalf("single writer lost updates: got %d/%d", len(serialDeps), len(agents))
	}
	// The direct run is expected to lose updates; that is the point of the test.
	// Do not fail if the scheduler happened to serialize it, but record it.
	if len(directDeps) == len(agents) {
		t.Logf("note: the direct race happened to serialize this run")
	}
}
