package tools

import (
	"testing"
)

func TestLookup_KnownTools(t *testing.T) {
	for _, name := range []string{"lezz", "adhd", "ocd-smoke-alarm", "tuner"} {
		tool, ok := Lookup(name)
		if !ok {
			t.Errorf("Lookup(%q) = false, want true", name)
			continue
		}
		if tool.Name != name {
			t.Errorf("Lookup(%q).Name = %q, want %q", name, tool.Name, name)
		}
		if tool.GithubSlug == "" {
			t.Errorf("Lookup(%q).GithubSlug is empty", name)
		}
	}
}

func TestLookup_UnknownTool(t *testing.T) {
	_, ok := Lookup("notarealtool")
	if ok {
		t.Error("Lookup(unknown) = true, want false")
	}
}

func TestNames_ContainsAllRegisteredTools(t *testing.T) {
	names := Names()
	if len(names) != len(Registry) {
		t.Errorf("Names() len = %d, want %d", len(names), len(Registry))
	}
	seen := make(map[string]bool)
	for _, n := range names {
		seen[n] = true
	}
	for _, tool := range Registry {
		if !seen[tool.Name] {
			t.Errorf("Names() missing %q", tool.Name)
		}
	}
}
