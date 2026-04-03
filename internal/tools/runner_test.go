package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFind_InLezzBin verifies Find returns a binary placed in ~/.lezz/bin
// before checking PATH.
func TestFind_InLezzBin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	binDir := filepath.Join(home, ".lezz", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(binDir, "adhd")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Find("adhd")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != binary {
		t.Errorf("Find = %q, want %q", got, binary)
	}
}

// TestFind_InPATH verifies Find falls through to PATH when ~/.lezz/bin is empty.
func TestFind_InPATH(t *testing.T) {
	// Use a fresh HOME with no .lezz/bin so the first search misses.
	t.Setenv("HOME", t.TempDir())

	pathDir := t.TempDir()
	binary := filepath.Join(pathDir, "adhd")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Prepend our temp dir to PATH.
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := Find("adhd")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != binary {
		t.Errorf("Find = %q, want %q", got, binary)
	}
}

// TestFind_LezzBinTakesPrecedenceOverPATH verifies ~/.lezz/bin wins when both exist.
func TestFind_LezzBinTakesPrecedenceOverPATH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lezzBin := filepath.Join(home, ".lezz", "bin")
	if err := os.MkdirAll(lezzBin, 0o755); err != nil {
		t.Fatal(err)
	}
	lezzBinary := filepath.Join(lezzBin, "adhd")
	if err := os.WriteFile(lezzBinary, []byte("#!/bin/sh\necho lezz"), 0o755); err != nil {
		t.Fatal(err)
	}

	pathDir := t.TempDir()
	pathBinary := filepath.Join(pathDir, "adhd")
	if err := os.WriteFile(pathBinary, []byte("#!/bin/sh\necho path"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := Find("adhd")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != lezzBinary {
		t.Errorf("Find = %q, want lezz-bin path %q", got, lezzBinary)
	}
}

// TestFind_NotFound verifies the error message names both search locations.
func TestFind_NotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Strip PATH to guarantee nothing is found.
	t.Setenv("PATH", "")

	_, err := Find("adhd")
	if err == nil {
		t.Fatal("Find: expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "~/.lezz/bin") {
		t.Errorf("error missing ~/.lezz/bin mention: %q", msg)
	}
	if !strings.Contains(msg, "lezz install adhd") {
		t.Errorf("error missing install hint: %q", msg)
	}
}

// TestRun_UnknownTool verifies Run rejects unregistered names immediately.
func TestRun_UnknownTool(t *testing.T) {
	err := Run("notarealtool", nil)
	if err == nil {
		t.Fatal("Run(unknown): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "notarealtool") {
		t.Errorf("error should name the tool: %q", err.Error())
	}
	// Should also list the managed tools.
	for _, name := range Names() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error should list managed tool %q: %q", name, err.Error())
		}
	}
}

// TestRun_ToolNotInstalled verifies Run returns a helpful error when the binary
// is not found, without panicking.
func TestRun_ToolNotInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")

	err := Run("adhd", nil)
	if err == nil {
		t.Fatal("Run(not installed): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "lezz install adhd") {
		t.Errorf("error should hint at install: %q", err.Error())
	}
}
