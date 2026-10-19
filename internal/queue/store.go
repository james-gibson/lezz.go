package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is store-neutral: callers must not depend on os.ErrNotExist.
var ErrNotFound = errors.New("queue: not found")

// Store is the queue of record. The worker writes through it; a file spool and
// a SQLite mirror are two drivers, per lab-safety#5's one-writer/two-drivers
// rule. The FIFO doorbell is deliberately outside this interface: it is a
// wakeup, not state.
type Store interface {
	PutJob(ctx context.Context, job *Job) error
	ListJobs(ctx context.Context) ([]*Job, error)
	DeleteJob(ctx context.Context, id string) error

	PutResult(ctx context.Context, res *Result) error
	GetResult(ctx context.Context, id string) (*Result, error)
	ListResults(ctx context.Context) ([]*Result, error)

	// AppendCommit records the durable history entry for a completed job.
	AppendCommit(ctx context.Context, job *Job, res *Result) error

	Name() string
	Close() error
}

// ---- file driver ----------------------------------------------------------

type fileStore struct {
	dir     string
	jobsDir string
	resDir  string
	dlqDir  string
	journal string
	fsync   bool
}

func newFileStore(dir string, fsync bool) (*fileStore, error) {
	f := &fileStore{
		dir:     dir,
		jobsDir: filepath.Join(dir, "jobs"),
		resDir:  filepath.Join(dir, "results"),
		dlqDir:  filepath.Join(dir, "dlq"),
		journal: filepath.Join(dir, "journal.jsonl"),
		fsync:   fsync,
	}
	for _, d := range []string{f.jobsDir, f.resDir, f.dlqDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("queue: mkdir %s: %w", d, err)
		}
	}
	return f, nil
}

func (f *fileStore) Name() string { return "file" }
func (f *fileStore) Close() error { return nil }

func (f *fileStore) PutJob(_ context.Context, job *Job) error {
	return writeJSONAtomic(filepath.Join(f.jobsDir, job.ID+".json"), job, f.fsync)
}

func (f *fileStore) ListJobs(_ context.Context) ([]*Job, error) {
	entries, err := os.ReadDir(f.jobsDir)
	if err != nil {
		return nil, err
	}
	jobs := make([]*Job, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(f.jobsDir, e.Name()))
		if readErr != nil {
			continue
		}
		var j Job
		if json.Unmarshal(data, &j) == nil {
			jobs = append(jobs, &j)
		}
	}
	sortJobs(jobs)
	return jobs, nil
}

func (f *fileStore) DeleteJob(_ context.Context, id string) error {
	err := os.Remove(filepath.Join(f.jobsDir, id+".json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (f *fileStore) PutResult(_ context.Context, res *Result) error {
	if err := writeJSONAtomic(filepath.Join(f.resDir, res.ID+".json"), res, f.fsync); err != nil {
		return err
	}
	if res.Error != "" {
		return writeJSONAtomic(filepath.Join(f.dlqDir, res.ID+".json"), res, f.fsync)
	}
	return nil
}

func (f *fileStore) GetResult(_ context.Context, id string) (*Result, error) {
	res, err := readResult(filepath.Join(f.resDir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return res, err
}

func (f *fileStore) ListResults(_ context.Context) ([]*Result, error) {
	entries, err := os.ReadDir(f.resDir)
	if err != nil {
		return nil, err
	}
	out := make([]*Result, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		if res, readErr := readResult(filepath.Join(f.resDir, e.Name())); readErr == nil {
			out = append(out, res)
		}
	}
	return out, nil
}

func (f *fileStore) AppendCommit(_ context.Context, job *Job, res *Result) error {
	fh, err := os.OpenFile(f.journal, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = fh.Close() }()
	entry := struct {
		At     time.Time `json:"at"`
		Job    *Job      `json:"job"`
		Result *Result   `json:"result"`
	}{At: time.Now().UTC(), Job: job, Result: res}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = fh.Write(append(data, '\n'))
	return err
}

// ---- mirror driver --------------------------------------------------------

// mirrorStore writes both drivers and reads the file driver as primary. On
// start, Reconcile copies missing records both ways and reports ids whose
// results disagree as torn — it does not pick a winner.
type mirrorStore struct {
	file   *fileStore
	sqlite *sqliteStore

	mu   sync.Mutex
	torn []string
}

func newMirrorStore(file *fileStore, sqlite *sqliteStore) *mirrorStore {
	return &mirrorStore{file: file, sqlite: sqlite}
}

func (m *mirrorStore) Name() string { return "file+sqlite" }

func (m *mirrorStore) Close() error {
	err := m.sqlite.Close()
	if cerr := m.file.Close(); err == nil {
		err = cerr
	}
	return err
}

func (m *mirrorStore) PutJob(ctx context.Context, job *Job) error {
	if err := m.file.PutJob(ctx, job); err != nil {
		return err
	}
	if err := m.sqlite.PutJob(ctx, job); err != nil {
		return fmt.Errorf("sqlite mirror: %w", err)
	}
	return nil
}

func (m *mirrorStore) ListJobs(ctx context.Context) ([]*Job, error) {
	return m.file.ListJobs(ctx)
}

func (m *mirrorStore) DeleteJob(ctx context.Context, id string) error {
	ferr := m.file.DeleteJob(ctx, id)
	serr := m.sqlite.DeleteJob(ctx, id)
	if ferr != nil {
		return ferr
	}
	return serr
}

func (m *mirrorStore) PutResult(ctx context.Context, res *Result) error {
	if err := m.file.PutResult(ctx, res); err != nil {
		return err
	}
	if err := m.sqlite.PutResult(ctx, res); err != nil {
		return fmt.Errorf("sqlite mirror: %w", err)
	}
	return nil
}

func (m *mirrorStore) GetResult(ctx context.Context, id string) (*Result, error) {
	res, err := m.file.GetResult(ctx, id)
	if err == nil {
		return res, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return m.sqlite.GetResult(ctx, id)
}

func (m *mirrorStore) ListResults(ctx context.Context) ([]*Result, error) {
	return m.file.ListResults(ctx)
}

func (m *mirrorStore) AppendCommit(ctx context.Context, job *Job, res *Result) error {
	if err := m.file.AppendCommit(ctx, job, res); err != nil {
		return err
	}
	return m.sqlite.AppendCommit(ctx, job, res)
}

// Reconcile compares the two drivers by id. Missing records are replayed to
// the other side; disagreeing results are reported as torn.
func (m *mirrorStore) Reconcile(ctx context.Context) ([]string, error) {
	fileJobs, err := m.file.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	sqliteJobs, err := m.sqlite.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	fileByID := make(map[string]*Job, len(fileJobs))
	for _, j := range fileJobs {
		fileByID[j.ID] = j
	}
	sqliteByID := make(map[string]*Job, len(sqliteJobs))
	for _, j := range sqliteJobs {
		sqliteByID[j.ID] = j
	}
	for id, j := range fileByID {
		if _, ok := sqliteByID[id]; !ok {
			if putErr := m.sqlite.PutJob(ctx, j); putErr != nil {
				return nil, fmt.Errorf("replay job %s to sqlite: %w", id, putErr)
			}
		}
	}
	for id, j := range sqliteByID {
		if _, ok := fileByID[id]; !ok {
			if putErr := m.file.PutJob(ctx, j); putErr != nil {
				return nil, fmt.Errorf("replay job %s to file: %w", id, putErr)
			}
		}
	}

	fileResults, err := m.file.ListResults(ctx)
	if err != nil {
		return nil, err
	}
	sqliteResults, err := m.sqlite.ListResults(ctx)
	if err != nil {
		return nil, err
	}
	fileResByID := make(map[string]*Result, len(fileResults))
	for _, r := range fileResults {
		fileResByID[r.ID] = r
	}
	sqliteResByID := make(map[string]*Result, len(sqliteResults))
	for _, r := range sqliteResults {
		sqliteResByID[r.ID] = r
	}
	torn := make([]string, 0)
	for id, r := range fileResByID {
		sr, ok := sqliteResByID[id]
		if !ok {
			if err := m.sqlite.PutResult(ctx, r); err != nil {
				return nil, fmt.Errorf("replay result %s to sqlite: %w", id, err)
			}
			continue
		}
		if resultsDiffer(r, sr) {
			torn = append(torn, id)
		}
	}
	for id, sr := range sqliteResByID {
		if _, ok := fileResByID[id]; !ok {
			if err := m.file.PutResult(ctx, sr); err != nil {
				return nil, fmt.Errorf("replay result %s to file: %w", id, err)
			}
		}
	}
	sort.Strings(torn)
	m.mu.Lock()
	m.torn = torn
	m.mu.Unlock()
	return torn, nil
}

func (m *mirrorStore) Torn() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.torn...)
}

func resultsDiffer(a, b *Result) bool {
	return a.Output != b.Output ||
		a.ExitCode != b.ExitCode ||
		a.Success != b.Success ||
		a.Error != b.Error ||
		a.Version != b.Version
}

func sortJobs(jobs []*Job) {
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].Priority != jobs[j].Priority {
			return jobs[i].Priority > jobs[j].Priority
		}
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
}
