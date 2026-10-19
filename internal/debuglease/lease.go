// Package debuglease issues and verifies time-boxed debug leases. A lease is
// signed with an SSH key (SSHSIG) and verified against an allowed_signers file.
// The default trust source is the operator's public GitHub SSH keys; when those
// are not configured, a local Ed25519 key and allowed-signers file are created
// with 0600 permissions.
package debuglease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const namespace = "lezz-debug"

// Lease is the signed permission to open a debug surface.
type Lease struct {
	Version   int       `json:"version"`
	SeedID    string    `json:"seed_id"`
	Actions   []string  `json:"actions"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Tick      int64     `json:"tick,omitempty"`
	Signer    string    `json:"signer"`
	Nonce     string    `json:"nonce,omitempty"`
}

var (
	seedPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	allowedActs = map[string]bool{"observe": true, "explain": true, "shadow": true}
	// ErrNotActive means the lease exists but its validity window does not
	// include the requested time.
	ErrNotActive = errors.New("debug lease is not active")
)

// New builds a lease for the given seed and TTL.
func New(seedID string, actions []string, ttl time.Duration, signer string) *Lease {
	now := time.Now().UTC()
	return &Lease{
		Version:   1,
		SeedID:    seedID,
		Actions:   actions,
		NotBefore: now,
		NotAfter:  now.Add(ttl),
		Signer:    signer,
	}
}

// Validate rejects a lease that is malformed or over-broad.
func (l *Lease) Validate() error {
	if l.Version != 1 {
		return fmt.Errorf("lease: unsupported version %d", l.Version)
	}
	if !seedPattern.MatchString(l.SeedID) {
		return fmt.Errorf("lease: invalid seed_id %q", l.SeedID)
	}
	if len(l.Actions) == 0 {
		return errors.New("lease: actions are required")
	}
	for _, a := range l.Actions {
		if !allowedActs[a] {
			return fmt.Errorf("lease: action %q is not allowed (observe, explain, shadow)", a)
		}
	}
	if l.Signer == "" {
		return errors.New("lease: signer is required")
	}
	if l.NotAfter.IsZero() || !l.NotAfter.After(l.NotBefore) {
		return errors.New("lease: not_after must be after not_before")
	}
	return nil
}

// Active reports whether the lease is valid at now.
func (l *Lease) Active(now time.Time) bool {
	return !now.Before(l.NotBefore) && now.Before(l.NotAfter)
}

// Load reads a lease and validates its shape. It does not verify the signature.
func Load(path string) (*Lease, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is a user-configured debug lease location.
	if err != nil {
		return nil, err
	}
	var l Lease
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("lease: parse %s: %w", path, err)
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &l, nil
}

// Write writes the lease atomically.
func (l *Lease) Write(path string) error {
	if err := l.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// Verify checks the detached SSHSIG at sigPath against allowedSigners for the
// given identity, then loads and validates the lease.
func Verify(leasePath, sigPath, allowedSigners, identity string) (*Lease, error) {
	if identity == "" {
		return nil, errors.New("lease: identity is required")
	}
	if _, err := os.Stat(sigPath); err != nil {
		return nil, fmt.Errorf("lease: signature %s: %w", sigPath, err)
	}
	leaseFile, err := os.Open(leasePath) //nolint:gosec // G304: path is a user-configured debug lease location.
	if err != nil {
		return nil, err
	}
	defer func() { _ = leaseFile.Close() }()
	cmd := exec.Command("ssh-keygen", "-Y", "verify", //nolint:gosec // G204: fixed binary, argv-only, no shell.
		"-f", allowedSigners, "-I", identity, "-n", namespace, "-s", sigPath)
	cmd.Stdin = leaseFile
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("lease: signature rejected: %s", strings.TrimSpace(string(out)))
	}
	return Load(leasePath)
}

// Sign signs leasePath with keyPath, writing leasePath+".sig". An existing
// signature is removed first: ssh-keygen prompts interactively on overwrite and
// otherwise leaves the stale signature in place.
func Sign(leasePath, keyPath string) error {
	sig := leasePath + ".sig"
	_ = os.Remove(sig)
	cmd := exec.Command("ssh-keygen", "-Y", "sign", "-f", keyPath, "-n", namespace, leasePath) //nolint:gosec // G204: fixed binary, argv-only, no shell.
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("lease: sign: %s", strings.TrimSpace(string(out)))
	}
	if _, err := os.Stat(sig); err != nil {
		return fmt.Errorf("lease: sign produced no signature: %w", err)
	}
	return nil
}

// AllowedLine renders one allowed_signers entry.
func AllowedLine(identity string, pubKey []byte) string {
	return fmt.Sprintf("%s namespaces=%q %s\n", identity, namespace, strings.TrimSpace(string(pubKey)))
}

// WriteAllowed writes an allowed_signers file from one public key. The file is
// 0600: it only holds public keys, but it is state private to this user.
func WriteAllowed(path, identity string, pubKey []byte) error {
	return os.WriteFile(path, []byte(AllowedLine(identity, pubKey)), 0o600)
}

// FetchGitHubKeys downloads https://github.com/<user>.keys. It does not pin;
// callers should cache the result and require an explicit refresh.
func FetchGitHubKeys(ctx context.Context, user string) ([]byte, error) {
	if !seedPattern.MatchString(user) {
		return nil, fmt.Errorf("github user %q is invalid", user)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/"+user+".keys", http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github keys: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(body), "ssh-") {
		return nil, errors.New("github keys: no SSH keys in response")
	}
	return body, nil
}

// FetchAndPin stores the GitHub keys once (TOFU) and returns the allowed-signers
// path. A refresh re-fetches and overwrites the pinned file.
func FetchAndPin(ctx context.Context, dir, user string, refresh bool) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "github-"+user+".allowed")
	if !refresh {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	keys, err := FetchGitHubKeys(ctx, user)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, line := range strings.Split(string(keys), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString(AllowedLine(user, []byte(line)))
	}
	if b.Len() == 0 {
		return "", errors.New("github keys: no usable keys")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// DefaultDir is ~/.lezz/keys.
func DefaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lezz", "keys")
}

// EnsureDefaultKey creates a local Ed25519 key and allowed-signers file if they
// do not exist. It is the "otherwise secure default": both files are 0600.
func EnsureDefaultKey(dir string) (keyPath, allowedPath string, err error) {
	if dir == "" {
		dir = DefaultDir()
	}
	if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
		return "", "", mkdirErr
	}
	keyPath = filepath.Join(dir, "debug_ed25519")
	allowedPath = filepath.Join(dir, "debug.allowed")
	pubPath := keyPath + ".pub"

	if _, err = os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
		cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "lezz-debug", "-f", keyPath) //nolint:gosec // G204: fixed binary, argv-only, no shell.
		if out, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
			return "", "", fmt.Errorf("generate default key: %s", strings.TrimSpace(string(out)))
		}
	}
	if chmodErr := os.Chmod(keyPath, 0o600); chmodErr != nil {
		return "", "", chmodErr
	}
	pub, err := os.ReadFile(pubPath) //nolint:gosec // G304: path is derived from the caller-supplied key directory.
	if err != nil {
		return "", "", err
	}
	if _, err = os.Stat(allowedPath); errors.Is(err, os.ErrNotExist) {
		if writeErr := WriteAllowed(allowedPath, "local", pub); writeErr != nil {
			return "", "", writeErr
		}
	}
	return keyPath, allowedPath, nil
}
