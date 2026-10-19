// Package seed reads and writes a project's seed.json: the stable project id,
// the cluster surface it joins, and an external auth reference. It never holds
// a secret and never touches a GitHub identity.
package seed

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/james-gibson/lezz.go/internal/credential"
)

// File is the on-disk seed. Secrets are references, never values.
type File struct {
	Version int     `json:"version"`
	Seed    Seed    `json:"seed"`
	Project Project `json:"project"`
	Auth    Auth    `json:"auth"`
}

// Seed is the stable project identity and the cluster surface it joins. Its id
// is generated locally and is not a GitHub identity.
type Seed struct {
	ID      string `json:"id"`      // stable project id; not a GitHub identity
	Surface string `json:"surface"` // cluster surface to join or create
	Start   Start  `json:"start"`   // declared 42i start; recomputed by see
}

// Start is the declared 42i position. A valid seed starts at 42+0i, rung 0.
type Start struct {
	Real      float64 `json:"real"`
	Imaginary float64 `json:"imaginary"`
	Rung      int     `json:"rung"`
}

// Project identifies the local project. Repo is provenance only and carries no
// authentication.
type Project struct {
	Name   string `json:"name"`
	Repo   string `json:"repo,omitempty"`    // provenance only, not auth
	MCPURL string `json:"mcp_url,omitempty"` // optional project surface
}

// Auth refers to a credential held outside the seed. Ref is a reference
// (env:NAME, file:/path, keychain:NAME), never the secret itself.
type Auth struct {
	Mode    string `json:"mode"`              // external
	Ref     string `json:"ref"`               // env:NAME, file:/path, keychain:...
	Subject string `json:"subject,omitempty"` // external id label, not a secret
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// New creates a seed with a fresh id and the 42i ground-truth start.
func New(projectName, surface string) (*File, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(projectName) == "" {
		projectName = "project"
	}
	if strings.TrimSpace(surface) == "" {
		surface = "base"
	}
	return &File{
		Version: 1,
		Seed: Seed{
			ID:      id,
			Surface: surface,
			Start:   Start{Real: 42, Imaginary: 0, Rung: 0},
		},
		Project: Project{Name: projectName},
		Auth:    Auth{Mode: "external", Ref: "env:LEZZ_CLUSTER_KEY"},
	}, nil
}

// Validate rejects a seed that cannot be used safely.
func (f *File) Validate() error {
	if f.Version != 1 {
		return fmt.Errorf("seed: unsupported version %d", f.Version)
	}
	if !idPattern.MatchString(f.Seed.ID) {
		return fmt.Errorf("seed: invalid id %q (use letters, digits, _ . -)", f.Seed.ID)
	}
	if strings.TrimSpace(f.Seed.Surface) == "" {
		return errors.New("seed: surface is required")
	}
	if f.Auth.Mode != "external" {
		return fmt.Errorf("seed: auth.mode must be external, got %q", f.Auth.Mode)
	}
	if strings.TrimSpace(f.Auth.Ref) == "" {
		return errors.New("seed: auth.ref is required (env:NAME, file:/path, keychain:...)")
	}
	if f.Project.Name == "" {
		return errors.New("seed: project.name is required")
	}
	if f.Seed.Start.Real != 42 || f.Seed.Start.Imaginary != 0 || f.Seed.Start.Rung != 0 {
		return errors.New("seed: start must be 42 + 0i at rung 0")
	}
	return nil
}

// Write writes the seed atomically with 0644. It must never contain a secret.
func (f *File) Write(path string) error {
	if err := f.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".seed-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	// seed.json carries only a credential reference, never a secret (see the
	// package contract), so it stays readable at the documented 0644.
	if err := os.Chmod(name, 0o644); err != nil { //nolint:gosec // G302: reference-only seed, no secret on disk.
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// Load reads and validates a seed.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the user's configured seed location.
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("seed: parse %s: %w", path, err)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// ResolveAuth checks that the external auth reference is present without
// returning the secret. An id alone is not proof; this only answers "is the
// referenced credential available?".
func ResolveAuth(ref string) (bool, error) {
	kind, value, ok := strings.Cut(ref, ":")
	if !ok || strings.TrimSpace(value) == "" {
		return false, fmt.Errorf("auth.ref must be env:NAME, file:/path, or keychain:NAME")
	}
	switch kind {
	case "env":
		return os.Getenv(value) != "", nil
	case "file":
		info, err := os.Stat(value)
		return err == nil && !info.IsDir(), nil
	case "keychain":
		// The keychain ref names a service; lezz's own items use the "lezz"
		// account. Check presence without retrieving the secret.
		return (credential.Keychain{}).Exists(value, "lezz")
	default:
		return false, fmt.Errorf("unsupported auth.ref kind %q", kind)
	}
}

func newID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:]), nil
}
