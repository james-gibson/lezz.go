package tools

// Tool describes a managed binary.
type Tool struct {
	Name       string // invocation name, e.g. "adhd"
	GithubSlug string // "owner/repo" on GitHub
	AssetName  string // release asset name template; empty = derive from name+platform
}

// Registry is the canonical list of tools lezz manages.
var Registry = []Tool{
	{
		Name:       "lezz",
		GithubSlug: "james-gibson/lezz.go",
	},
	{
		Name:       "adhd",
		GithubSlug: "james-gibson/adhd",
	},
	{
		Name:       "ocd-smoke-alarm",
		GithubSlug: "james-gibson/ocd-smoke-alarm",
	},
	{
		Name:       "tuner",
		GithubSlug: "james-gibson/tuner",
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

// Names returns the names of all managed tools.
func Names() []string {
	names := make([]string, len(Registry))
	for i, t := range Registry {
		names[i] = t.Name
	}
	return names
}
