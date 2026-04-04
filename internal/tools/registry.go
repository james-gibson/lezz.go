package tools

// Tool describes a managed binary.
type Tool struct {
	Name       string          // invocation name, e.g. "adhd"
	GithubSlug string          // "owner/repo" on GitHub
	AssetName  string          // release asset name template; empty = derive from name+platform
	Profiles   []DaemonProfile // available daemon profiles; empty = not daemonisable
}

// DaemonProfile describes one way to run a tool as a background service.
type DaemonProfile struct {
	Name        string   // profile name, e.g. "idle"
	Description string   // human-readable purpose
	Args        []string // extra args appended after the binary path
}

// Registry is the canonical list of tools lezz manages.
var Registry = []Tool{
	{
		Name:       "lezz",
		GithubSlug: "james-gibson/lezz.go",
		Profiles: []DaemonProfile{
			{
				Name:        "demo",
				Description: "self-contained demo cluster — 2 smoke-alarm instances + adhd headless",
				Args:        []string{"demo"},
			},
		},
	},
	{
		Name:       "adhd",
		GithubSlug: "james-gibson/adhd",
		Profiles: []DaemonProfile{
			{
				Name:        "idle",
				Description: "headless mode — running but not connected to any smoke-alarm",
				Args:        []string{"--headless"},
			},
		},
	},
	{
		Name:       "ocd-smoke-alarm",
		GithubSlug: "james-gibson/smoke-alarm",
		Profiles: []DaemonProfile{
			{
				Name:        "idle",
				Description: "running with no targets configured",
				Args:        []string{},
			},
		},
	},
	{
		Name:       "tuner",
		GithubSlug: "james-gibson/tuner",
		Profiles: []DaemonProfile{
			{
				Name:        "idle",
				Description: "running with no configuration",
				Args:        []string{},
			},
		},
	},
}

// Lookup returns the Tool for the given name, or false if not managed.
func Lookup(name string) (Tool, bool) {
	for _, t := range Registry {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// LookupProfile returns the named profile for a tool, or false if not found.
func LookupProfile(t Tool, profileName string) (DaemonProfile, bool) {
	for _, p := range t.Profiles {
		if p.Name == profileName {
			return p, true
		}
	}
	return DaemonProfile{}, false
}

// Names returns the names of all managed tools.
func Names() []string {
	names := make([]string, len(Registry))
	for i, t := range Registry {
		names[i] = t.Name
	}
	return names
}
