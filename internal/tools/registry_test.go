package tools

import (
	"net"
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

// TestAdhdIdleProfile_ServesMCPOnLoopback guards issue #11. A bare --headless
// loads config.DefaultConfig(), whose MCPServer.Enabled is false, so the
// installed service runs but exposes no MCP server on any port. The idle
// profile must therefore pass an explicit --mcp-addr — and that address must be
// loopback, because the MCP surface has no inbound auth, so ":9090" would
// publish it on 0.0.0.0 rather than to this host.
func TestAdhdIdleProfile_ServesMCPOnLoopback(t *testing.T) {
	tool, ok := Lookup("adhd")
	if !ok {
		t.Fatal(`Lookup("adhd") = false, want true`)
	}
	profile, ok := LookupProfile(tool, "idle")
	if !ok {
		t.Fatal(`LookupProfile(adhd, "idle") = false, want true`)
	}

	var (
		headless bool
		addr     string
	)
	for i := 0; i < len(profile.Args); i++ {
		switch profile.Args[i] {
		case "--headless":
			headless = true
		case "--mcp-addr":
			if i+1 >= len(profile.Args) {
				t.Fatalf("--mcp-addr has no value in args %q", profile.Args)
			}
			addr = profile.Args[i+1]
			i++
		}
	}
	if !headless {
		t.Errorf("adhd idle args %q drop --headless; the service should run without a TUI", profile.Args)
	}
	if addr == "" {
		t.Fatalf("adhd idle args %q pass no --mcp-addr; --headless alone leaves the MCP server disabled", profile.Args)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("--mcp-addr %q is not host:port: %v", addr, err)
	}
	if port == "" || port == "0" {
		t.Errorf("--mcp-addr %q binds an OS-assigned port; the profile needs a stable, discoverable one", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Errorf("--mcp-addr %q is not loopback; the MCP server has no inbound auth, so a non-loopback bind publishes it", addr)
	}
}
