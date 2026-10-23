// Command site renders the published documentation site for lezz.go.
//
// The source of truth is the repository itself: every markdown file under
// docs/ becomes a page, and landing.html is the sales page. There is no
// second copy of the documentation to drift, and no site framework to pin —
// the whole generator is this package plus goldmark, built with the same Go
// toolchain as the CLI.
//
// It writes a self-contained directory (HTML, one stylesheet, images) that the
// Pages workflow uploads as an artifact. Relative links between documents are
// rewritten from .md to the page that was actually generated.
package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Embedded so the generator can be run from any working directory, and so the
// templates and assets cannot be separated from the code that reads them.
//
//go:embed layout.html landing.html static
var assets embed.FS

func main() {
	var (
		docsDir = flag.String("docs", "../docs", "markdown documentation directory")
		outDir  = flag.String("out", "../dist/site", "output directory for the rendered site")
		base    = flag.String("base", "/lezz.go/", "absolute URL prefix the site is served under")
		version = flag.String("version", "dev", "release version shown in the footer")
	)
	flag.Parse()

	if err := run(*docsDir, *outDir, normalizeBase(*base), *version); err != nil {
		fmt.Fprintf(os.Stderr, "site: %v\n", err)
		os.Exit(1)
	}
}

func run(docsDir, outDir, base, version string) error {
	pages, err := loadDocs(docsDir)
	if err != nil {
		return err
	}
	if len(pages) == 0 {
		// A silent empty site is worse than a failed build: the deploy would
		// publish a landing page whose docs index renders nothing.
		return fmt.Errorf("no markdown files found under %s", docsDir)
	}

	layout, err := parseLayout()
	if err != nil {
		return err
	}

	data := siteData{
		Base:       base,
		Version:    version,
		RepoURL:    "https://github.com/james-gibson/lezz.go",
		SeaURL:     "https://github.com/james-gibson/lezz.sea",
		SeaRelease: "https://github.com/james-gibson/lezz.sea/releases/download/dev/lezz.sea.app.zip",
		Releases:   "https://github.com/james-gibson/lezz.go/releases",
		Sections:   buildNav(pages, base),
	}

	md := newMarkdown()

	for _, p := range pages {
		body, err := renderDoc(md, docsDir, p, base)
		if err != nil {
			return fmt.Errorf("%s: %w", p.Source, err)
		}
		page := pageData{
			siteData:    data,
			Title:       p.Title + " — lezz",
			Description: firstParagraph(p.Body),
			BodyClass:   "docs",
			Current:     p.Out,
			Content:     body,
		}
		if err := writePage(layout, outDir, p.Out, page); err != nil {
			return err
		}
	}

	landing, err := renderLanding(layout, data)
	if err != nil {
		return err
	}
	if err := writePage(layout, outDir, "index.html", landing); err != nil {
		return err
	}

	return copyStatic(outDir)
}

// normalizeBase makes a prefix usable in templates: exactly one leading and
// trailing slash, and "/" when the site is served from a domain root.
func normalizeBase(b string) string {
	b = strings.Trim(strings.TrimSpace(b), "/")
	if b == "" {
		return "/"
	}
	return "/" + b + "/"
}

func writePage(layout *template.Template, outDir, rel string, page pageData) error {
	var buf strings.Builder
	if err := layout.Execute(&buf, page); err != nil {
		return fmt.Errorf("render %s: %w", rel, err)
	}
	dst := filepath.Join(outDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(buf.String()), 0o644)
}

// copyStatic writes the stylesheet and images next to the pages.
func copyStatic(outDir string) error {
	return fs.WalkDir(assets, "static", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := assets.ReadFile(name)
		if err != nil {
			return err
		}
		dst := filepath.Join(outDir, "assets", filepath.FromSlash(path.Base(name)))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

// buildNav groups pages into the two sections the sidebar shows. A page with
// no section (only possible if docs/ grows a new subdirectory) is folded into
// the first section rather than silently disappearing from the nav.
func buildNav(pages []docPage, base string) []navSection {
	order := []string{"Guides", "Decisions"}
	groups := map[string][]navItem{}
	for _, p := range pages {
		item := navItem{Title: p.Title, URL: base + p.Out}
		groups[p.Section] = append(groups[p.Section], item)
	}

	var out []navSection
	for _, name := range order {
		if items := groups[name]; len(items) > 0 {
			sort.Slice(items, func(i, j int) bool {
				return strings.ToLower(items[i].Title) < strings.ToLower(items[j].Title)
			})
			out = append(out, navSection{Name: name, Items: items})
		}
	}
	return out
}
