package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/james-gibson/isotope"
)

// TestDeskFeatureIsTheOrigin pins the le.ad empty-desk declaration. The
// shell-to-gherkin validator read features/seed/le_ad_desk.sh into this file
// and produced isotope XnWmP4EMW6C3QTibqeRpd3EsU60UZxoq8wRwRRJBmxU
// (20 tokens, 3 parts, signature 0c3ab2637a830dfd…); the handler is its
// inversion. The signature pins the canonical raw stream just as the
// validator's isotope ID does.
func TestDeskFeatureIsTheOrigin(t *testing.T) {
	path := filepath.Join("..", "..", "features", "seed", "le-ad-desk.feature")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read origin: %v", err)
	}
	text := string(raw)

	first, err := isotope.CanonicalizeGherkinToIsotope(text, []byte("seed-42"), "wan-ingress", 1)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	second, err := isotope.CanonicalizeGherkinToIsotope(text, []byte("seed-42"), "wan-ingress", 1)
	if err != nil || second.Signature != first.Signature || second.Raw != first.Raw {
		t.Fatalf("canon is not stable: first=%+v second=%+v err=%v", first, second, err)
	}
	if got := first.Signature; got != "0c3ab2637a830dfd674ef20469df19d580d6723e4068bdf2a036261071d88f0d" {
		t.Fatalf("origin signature = %s, want 0c3ab2637a830dfd…", got)
	}
	if strings.TrimSpace(first.Raw) == "" {
		t.Fatal("canon produced an empty raw stream")
	}
}
