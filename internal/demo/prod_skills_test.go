package demo

import (
	"path/filepath"
	"strings"
	"testing"
)

// A skill may be nothing more than a transient environment binding: it is
// applied to the generation's flow, and it is gone when the generation is
// replaced. The tear ledger is the first one, and it must be per node and
// stable across generations or the seed it carries is useless.
func TestProdSkillsSeedAPerNodeTearLedger(t *testing.T) {
	root := t.TempDir()
	a := prodNode{Name: "alarm-a-a"}
	b := prodNode{Name: "alarm-c"}

	envA := skillEnviron(prodSkillsFor(root, a))
	envB := skillEnviron(prodSkillsFor(root, b))
	if len(envA) != 1 || len(envB) != 1 {
		t.Fatalf("want one env binding each, got %v and %v", envA, envB)
	}
	if envA[0] == envB[0] {
		t.Fatalf("nodes share a ledger: %q", envA[0])
	}
	for _, entry := range []string{envA[0], envB[0]} {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key != "ADHD_TEAR_LEDGER_PATH" {
			t.Fatalf("unexpected binding %q", entry)
		}
		if !filepath.IsAbs(value) {
			t.Fatalf("ledger path is not absolute: %q", value)
		}
		if !strings.HasPrefix(value, root) {
			t.Fatalf("ledger %q is not under the stable state dir %q", value, root)
		}
	}

	// The same node and root produce the same seed path in every generation,
	// which is what lets a rotation child inherit the parent's ledger.
	if again := skillEnviron(prodSkillsFor(root, a)); again[0] != envA[0] {
		t.Fatalf("seed path moved between generations: %q -> %q", envA[0], again[0])
	}
}

func TestSkillEnvironIsDeterministic(t *testing.T) {
	skills := []prodSkill{
		{Name: "z", Env: map[string]string{"ZED": "1", "ALPHA": "2"}},
		{Name: "a", Env: map[string]string{"MID": "3"}},
	}
	got := skillEnviron(skills)
	want := []string{"ALPHA=2", "MID=3", "ZED=1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
