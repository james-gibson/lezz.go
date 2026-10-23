package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/creativeprojects/go-selfupdate"
)

// The wiring tests prove a validator is configured. This one proves it is
// load-bearing: a release whose bytes do not match its own checksums.txt must
// not reach ~/.lezz/bin.

type fakeSource struct {
	archive   []byte
	checksums []byte
	assetName string
	assetID   int64
	checkID   int64
}

func (s *fakeSource) ListReleases(context.Context, selfupdate.Repository) ([]selfupdate.SourceRelease, error) {
	return []selfupdate.SourceRelease{&fakeRelease{
		tag: "v0.1.0",
		assets: []selfupdate.SourceAsset{
			&fakeAsset{id: s.assetID, name: s.assetName, size: len(s.archive)},
			&fakeAsset{id: s.checkID, name: "checksums.txt", size: len(s.checksums)},
		},
	}}, nil
}

func (s *fakeSource) DownloadReleaseAsset(_ context.Context, _ *selfupdate.Release, assetID int64) (io.ReadCloser, error) {
	switch assetID {
	case s.assetID:
		return io.NopCloser(bytes.NewReader(s.archive)), nil
	case s.checkID:
		return io.NopCloser(bytes.NewReader(s.checksums)), nil
	default:
		return nil, fmt.Errorf("unknown asset %d", assetID)
	}
}

type fakeRelease struct {
	tag    string
	assets []selfupdate.SourceAsset
}

func (r *fakeRelease) GetID() int64                        { return 1 }
func (r *fakeRelease) GetTagName() string                  { return r.tag }
func (r *fakeRelease) GetDraft() bool                      { return false }
func (r *fakeRelease) GetPrerelease() bool                 { return false }
func (r *fakeRelease) GetPublishedAt() time.Time           { return time.Now().Add(-time.Hour) }
func (r *fakeRelease) GetReleaseNotes() string             { return "" }
func (r *fakeRelease) GetName() string                     { return r.tag }
func (r *fakeRelease) GetURL() string                      { return "https://example.invalid" }
func (r *fakeRelease) GetAssets() []selfupdate.SourceAsset { return r.assets }

type fakeAsset struct {
	id   int64
	name string
	size int
}

func (a *fakeAsset) GetID() int64                  { return a.id }
func (a *fakeAsset) GetName() string               { return a.name }
func (a *fakeAsset) GetSize() int                  { return a.size }
func (a *fakeAsset) GetBrowserDownloadURL() string { return "https://example.invalid/" + a.name }

// tarGz builds a release archive containing a file named name.
func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func downloadWith(t *testing.T, archive, checksums []byte) (string, error) {
	t.Helper()
	assetName := fmt.Sprintf("adhd_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	src := &fakeSource{
		archive:   archive,
		checksums: checksums,
		assetName: assetName,
		assetID:   1,
		checkID:   2,
	}

	cfg := checksumConfig()
	cfg.Source = src
	cfg.OS = runtime.GOOS
	cfg.Arch = runtime.GOARCH
	updater, err := selfupdate.NewUpdater(cfg)
	if err != nil {
		t.Fatalf("new updater: %v", err)
	}

	release, found, err := updater.DetectLatest(context.Background(), selfupdate.ParseSlug("james-gibson/adhd"))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !found {
		t.Fatalf("no release detected for %s/%s asset %s", runtime.GOOS, runtime.GOARCH, assetName)
	}

	dest := filepath.Join(t.TempDir(), "adhd")
	if err := os.WriteFile(dest, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updater.UpdateTo(context.Background(), release, dest); err != nil {
		return dest, err
	}
	return dest, nil
}

func TestDownloadRejectsABadChecksum(t *testing.T) {
	archive := tarGz(t, "adhd", []byte("#!/bin/sh\necho verified\n"))
	assetName := fmt.Sprintf("adhd_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive)
	// Same shape as goreleaser's checksums.txt, wrong digest.
	digest := hex.EncodeToString(sum[:])
	flipped := "0"
	if digest[0] == '0' {
		flipped = "1"
	}
	bad := fmt.Sprintf("%s  %s\n", flipped+digest[1:], assetName)

	_, err := downloadWith(t, archive, []byte(bad))
	if err == nil {
		t.Fatal("installed an archive that did not match its checksums.txt")
	}
}

func TestDownloadRejectsAMissingChecksumsFile(t *testing.T) {
	archive := tarGz(t, "adhd", []byte("#!/bin/sh\necho verified\n"))

	_, err := downloadWith(t, archive, nil)
	if err == nil {
		t.Fatal("installed a release that publishes no checksums.txt")
	}
}

func TestDownloadAcceptsAMatchingChecksum(t *testing.T) {
	body := []byte("#!/bin/sh\necho verified\n")
	archive := tarGz(t, "adhd", body)
	assetName := fmt.Sprintf("adhd_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive)
	good := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)

	dest, err := downloadWith(t, archive, []byte(good))
	if err != nil {
		t.Fatalf("verified download failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("installed bytes are %q, want %q", got, body)
	}
}
