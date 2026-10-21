package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

// ---- fileFetcher / installViaFetcher tests ----------------------------------

func TestInstallViaFetcher_PlacesBinaryInBinDir(t *testing.T) {
	// Create a fake binary to "download".
	src := filepath.Join(t.TempDir(), "fakebinary")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	lezzBinDir := t.TempDir()
	fetcher := &fileFetcher{src: src, version: "1.2.3"}

	ver, err := installViaFetcher(context.Background(), fetcher, "adhd", lezzBinDir)
	if err != nil {
		t.Fatalf("installViaFetcher: %v", err)
	}
	if ver != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", ver)
	}

	installed := filepath.Join(lezzBinDir, "adhd")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("installed binary not found: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("installed binary is not executable")
	}
}

func TestInstallViaFetcher_BinaryIsExecutable(t *testing.T) {
	// Write a real shell script as the "download".
	src := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho hello-from-fetcher"), 0o755); err != nil {
		t.Fatal(err)
	}

	lezzBinDir := t.TempDir()
	if _, err := installViaFetcher(context.Background(), &fileFetcher{src: src, version: "0.1.0"}, "tool", lezzBinDir); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(filepath.Join(lezzBinDir, "tool")).Output()
	if err != nil {
		t.Fatalf("run installed binary: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "hello-from-fetcher" {
		t.Errorf("output = %q, want hello-from-fetcher", got)
	}
}

func TestInstallViaFetcher_OverwritesExistingBinary(t *testing.T) {
	lezzBinDir := t.TempDir()

	writeAndInstall := func(content, version string) {
		src := filepath.Join(t.TempDir(), "tool")
		_ = os.WriteFile(src, []byte(content), 0o755)
		if _, err := installViaFetcher(context.Background(), &fileFetcher{src: src, version: version}, "tool", lezzBinDir); err != nil {
			t.Fatalf("installViaFetcher(%s): %v", version, err)
		}
	}

	writeAndInstall("#!/bin/sh\necho v1", "1.0.0")
	writeAndInstall("#!/bin/sh\necho v2", "2.0.0")

	out, err := exec.Command(filepath.Join(lezzBinDir, "tool")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "v2" {
		t.Errorf("after overwrite output = %q, want v2", got)
	}
}

func TestInstallViaFetcher_MissingSourceFile(t *testing.T) {
	_, err := installViaFetcher(
		context.Background(),
		&fileFetcher{src: "/nonexistent/path/tool", version: "1.0.0"},
		"tool",
		t.TempDir(),
	)
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
}

// TestLinkFromGOPATH_HappyPath verifies that linkFromGOPATH creates a symlink
// from binDir/<name> pointing at GOPATH/bin/<name>, and that the symlink resolves.
func TestLinkFromGOPATH_HappyPath(t *testing.T) {
	fakeGOPATH := t.TempDir()
	gopathBin := filepath.Join(fakeGOPATH, "bin")
	if err := os.MkdirAll(gopathBin, 0o755); err != nil {
		t.Fatal(err)
	}

	// Place a fake binary in GOPATH/bin.
	src := filepath.Join(gopathBin, "mytool")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho mytool"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GOPATH", fakeGOPATH)

	lezzBinDir := t.TempDir()
	ctx := context.Background()

	ver, err := linkFromGOPATH(ctx, "mytool", lezzBinDir)
	if err != nil {
		t.Fatalf("linkFromGOPATH: %v", err)
	}
	if ver != "latest" {
		t.Errorf("version = %q, want %q", ver, "latest")
	}

	dest := filepath.Join(lezzBinDir, "mytool")
	resolved, err := filepath.EvalSymlinks(dest)
	if err != nil {
		t.Fatalf("symlink not resolvable: %v", err)
	}
	// EvalSymlinks resolves all path components including OS-level symlinks
	// (e.g. /var → /private/var on macOS), so normalise both sides.
	wantResolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatalf("EvalSymlinks(src): %v", err)
	}
	if resolved != wantResolved {
		t.Errorf("symlink resolves to %q, want %q", resolved, wantResolved)
	}
}

// TestLinkFromGOPATH_BinaryMissing verifies a clear error when the binary is
// absent from GOPATH/bin (e.g. go install silently did nothing).
func TestLinkFromGOPATH_BinaryMissing(t *testing.T) {
	fakeGOPATH := t.TempDir()
	t.Setenv("GOPATH", fakeGOPATH)
	// Do NOT create GOPATH/bin/mytool.

	_, err := linkFromGOPATH(context.Background(), "mytool", t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
	if !strings.Contains(err.Error(), "binary not found") {
		t.Errorf("error should mention binary not found: %q", err.Error())
	}
}

// TestLinkFromGOPATH_OverwritesExisting verifies that calling linkFromGOPATH a
// second time replaces the old symlink rather than returning an error.
func TestLinkFromGOPATH_OverwritesExisting(t *testing.T) {
	fakeGOPATH := t.TempDir()
	gopathBin := filepath.Join(fakeGOPATH, "bin")
	_ = os.MkdirAll(gopathBin, 0o755)
	_ = os.WriteFile(filepath.Join(gopathBin, "mytool"), []byte("v2"), 0o755)
	t.Setenv("GOPATH", fakeGOPATH)

	lezzBinDir := t.TempDir()
	ctx := context.Background()

	// First install.
	if _, err := linkFromGOPATH(ctx, "mytool", lezzBinDir); err != nil {
		t.Fatalf("first linkFromGOPATH: %v", err)
	}
	// Second install (simulates re-running lezz install).
	if _, err := linkFromGOPATH(ctx, "mytool", lezzBinDir); err != nil {
		t.Fatalf("second linkFromGOPATH: %v", err)
	}
}

// TestGHReleaseFetcher_InstallsMockRelease verifies that ghReleaseFetcher
// downloads an asset from a mock Source, writes it to binDir, and returns the
// version string — without touching real GitHub.
func TestGHReleaseFetcher_InstallsMockRelease(t *testing.T) {
	fakeBinary := []byte("#!/bin/sh\necho mock-gh-release\n")

	// Asset name must carry the current OS+arch suffix so go-selfupdate's
	// findAssetFromRelease selects it.
	assetName := fmt.Sprintf("fakerepo_%s_%s", runtime.GOOS, runtime.GOARCH)

	src := &mockReleaseSource{
		tagName:    "v1.5.0",
		assetName:  assetName,
		assetBytes: fakeBinary,
	}

	lezzBinDir := t.TempDir()
	fetcher := &ghReleaseFetcher{
		slug:   "testowner/fakerepo",
		config: selfupdate.Config{Source: src},
	}

	ver, err := installViaFetcher(context.Background(), fetcher, "fakerepo", lezzBinDir)
	if err != nil {
		t.Fatalf("installViaFetcher: %v", err)
	}
	// go-selfupdate strips the "v" prefix from the semver tag.
	if ver != "1.5.0" {
		t.Errorf("version = %q, want 1.5.0", ver)
	}

	installed := filepath.Join(lezzBinDir, "fakerepo")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("installed binary not found: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("installed binary is not executable")
	}
}

// TestGHReleaseFetcher_NoReleasesFound verifies a clear error when the mock
// source returns no releases.
func TestGHReleaseFetcher_NoReleasesFound(t *testing.T) {
	src := &mockReleaseSource{} // empty — no releases

	fetcher := &ghReleaseFetcher{
		slug:   "testowner/emptytool",
		config: selfupdate.Config{Source: src},
	}

	_, err := installViaFetcher(context.Background(), fetcher, "emptytool", t.TempDir())
	if err == nil {
		t.Fatal("expected error for empty release list, got nil")
	}
	if !strings.Contains(err.Error(), "no releases found") {
		t.Errorf("error should mention no releases found: %q", err.Error())
	}
}

// TestGHReleaseFetcher_NoReleasesIsNoAsset establishes the only condition that
// may become a source build: a repository with nothing to download.
func TestGHReleaseFetcher_NoReleasesIsNoAsset(t *testing.T) {
	fetcher := &ghReleaseFetcher{
		slug:   "testowner/emptytool",
		config: selfupdate.Config{Source: &mockReleaseSource{}},
	}

	_, err := installViaFetcher(context.Background(), fetcher, "emptytool", t.TempDir())
	if !errors.Is(err, ErrNoReleaseAsset) {
		t.Fatalf("err = %v, want ErrNoReleaseAsset", err)
	}
}

// TestGHReleaseFetcher_APIErrorIsNotNoAsset is the #36 regression: a rejected
// credential must not look like "this tool has no release", because Install used
// to answer that with `go install` and report "latest".
func TestGHReleaseFetcher_APIErrorIsNotNoAsset(t *testing.T) {
	fetcher := &ghReleaseFetcher{
		slug: "testowner/fakerepo",
		config: selfupdate.Config{Source: &mockReleaseSource{
			listErr: errors.New("GET https://api.github.com/repos/testowner/fakerepo/releases: 401 Bad credentials []"),
		}},
	}

	_, err := installViaFetcher(context.Background(), fetcher, "fakerepo", t.TempDir())
	if err == nil {
		t.Fatal("expected the API error to surface, got nil")
	}
	if errors.Is(err, ErrNoReleaseAsset) {
		t.Error("an API error was classified as a missing asset, so Install would silently build from source")
	}
}

// TestInstallInto_DoesNotFallBackOnAPIError drives the decision itself: the
// fetcher fails with an auth error, so installInto must return it rather than
// reaching for the toolchain.
func TestInstallInto_DoesNotFallBackOnAPIError(t *testing.T) {
	binDir := t.TempDir()
	tool := Tool{Name: "fakerepo", GithubSlug: "testowner/fakerepo"}
	fetcher := &ghReleaseFetcher{
		slug: "testowner/fakerepo",
		config: selfupdate.Config{Source: &mockReleaseSource{
			listErr: errors.New("GET https://api.github.com/repos/testowner/fakerepo/releases: 401 Bad credentials []"),
		}},
	}

	_, err := installInto(context.Background(), fetcher, tool, binDir)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "lezz auth github") {
		t.Errorf("error should point at `lezz auth github`: %q", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(binDir, "fakerepo")); statErr == nil {
		t.Error("a binary was installed anyway")
	}
}

// TestModuleVersion_NonGoBinary returns "" rather than garbage for a file with
// no build info.
func TestModuleVersion_NonGoBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notgo")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := moduleVersion(context.Background(), path); got != "" {
		t.Errorf("moduleVersion = %q, want empty", got)
	}
}

// ---- mock Source / SourceRelease / SourceAsset types -------------------------

// mockReleaseSource implements selfupdate.Source.
// It returns a single release with one asset whose bytes are served verbatim.
type mockReleaseSource struct {
	tagName    string // e.g. "v1.5.0"; empty → no releases
	assetName  string // e.g. "fakerepo_linux_amd64"
	assetBytes []byte
	listErr    error // simulates an API failure (401, rate limit, network)
}

func (s *mockReleaseSource) ListReleases(_ context.Context, _ selfupdate.Repository) ([]selfupdate.SourceRelease, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.tagName == "" {
		return nil, nil
	}
	return []selfupdate.SourceRelease{
		&mockRelease{
			tagName: s.tagName,
			asset:   &mockAsset{name: s.assetName, content: s.assetBytes},
		},
	}, nil
}

func (s *mockReleaseSource) DownloadReleaseAsset(_ context.Context, _ *selfupdate.Release, _ int64) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.assetBytes)), nil
}

// mockRelease implements selfupdate.SourceRelease.
type mockRelease struct {
	tagName string
	asset   selfupdate.SourceAsset
}

func (r *mockRelease) GetID() int64                        { return 1 }
func (r *mockRelease) GetTagName() string                  { return r.tagName }
func (r *mockRelease) GetDraft() bool                      { return false }
func (r *mockRelease) GetPrerelease() bool                 { return false }
func (r *mockRelease) GetPublishedAt() time.Time           { return time.Now() }
func (r *mockRelease) GetReleaseNotes() string             { return "" }
func (r *mockRelease) GetName() string                     { return r.tagName }
func (r *mockRelease) GetURL() string                      { return "https://example.com/releases/1" }
func (r *mockRelease) GetAssets() []selfupdate.SourceAsset { return []selfupdate.SourceAsset{r.asset} }

// mockAsset implements selfupdate.SourceAsset.
type mockAsset struct {
	name    string
	content []byte
}

func (a *mockAsset) GetID() int64                  { return 42 }
func (a *mockAsset) GetName() string               { return a.name }
func (a *mockAsset) GetSize() int                  { return len(a.content) }
func (a *mockAsset) GetBrowserDownloadURL() string { return "https://example.com/download/" + a.name }

// TestInstallViaGoInstall_Integration builds a real tiny Go binary into a
// temporary GOPATH and verifies that installViaGoInstall creates a working
// symlink in lezzBinDir.
//
// This test requires the `go` toolchain to be available.
func TestInstallViaGoInstall_Integration(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	// Build a real minimal binary directly into a fake GOPATH/bin.
	// We use `go build` rather than `go install` here so we can target a
	// local source tree without a published module version.
	fakeGOPATH := t.TempDir()
	gopathBin := filepath.Join(fakeGOPATH, "bin")
	_ = os.MkdirAll(gopathBin, 0o755)

	// Write a minimal main package.
	srcDir := filepath.Join(t.TempDir(), "lezz-integ-tool")
	_ = os.MkdirAll(srcDir, 0o755)
	_ = os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(
		`package main
import "fmt"
func main() { fmt.Println("lezz-integ-tool ok") }
`), 0o644)
	_ = os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte(
		"module lezz-integ-tool\ngo 1.21\n",
	), 0o644)

	builtBin := filepath.Join(gopathBin, "lezz-integ-tool")
	buildCmd := exec.Command("go", "build", "-o", builtBin, ".")
	buildCmd.Dir = srcDir
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build test tool: %v\n%s", err, out)
	}

	// Now test linkFromGOPATH (the post-install step of installViaGoInstall).
	t.Setenv("GOPATH", fakeGOPATH)
	lezzBinDir := t.TempDir()

	ver, err := linkFromGOPATH(context.Background(), "lezz-integ-tool", lezzBinDir)
	if err != nil {
		t.Fatalf("linkFromGOPATH: %v", err)
	}
	if ver != "latest" {
		t.Errorf("version = %q, want latest", ver)
	}

	// Verify the symlinked binary actually runs.
	symlinked := filepath.Join(lezzBinDir, "lezz-integ-tool")
	out, err := exec.Command(symlinked).Output()
	if err != nil {
		t.Fatalf("run symlinked binary: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "lezz-integ-tool ok" {
		t.Errorf("binary output = %q, want %q", got, "lezz-integ-tool ok")
	}
}
