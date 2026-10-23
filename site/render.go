package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// docPage is one markdown file and the page it becomes.
type docPage struct {
	Source  string // path relative to the docs root, slash form ("adr/README.md")
	Out     string // path relative to the site root ("docs/adr/index.html")
	Title   string
	Section string // sidebar group
	Body    []byte
}

type navItem struct {
	Title string
	URL   string
}

type navSection struct {
	Name  string
	Items []navItem
}

// siteData is shared by every page; pageData is one page.
type siteData struct {
	Base       string
	Version    string
	RepoURL    string
	SeaURL     string
	SeaRelease string
	Releases   string
	Sections   []navSection
}

type pageData struct {
	siteData
	Title       string
	Description string
	BodyClass   string
	Current     string
	Content     template.HTML
}

// IsCurrent marks the nav entry for the page being rendered.
func (p pageData) IsCurrent(url string) bool {
	return p.Current != "" && url == p.Base+p.Current
}

func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		// GFM is what these documents are written in: the tables in
		// docs/attestation.md and the task lists would otherwise render as
		// paragraphs. Unsafe stays on because the sources are repository
		// content, not user input — they already merge as HTML on GitHub.
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
}

func parseLayout() (*template.Template, error) {
	l, err := assets.ReadFile("layout.html")
	if err != nil {
		return nil, err
	}
	t, err := template.New("layout").Parse(string(l))
	if err != nil {
		return nil, fmt.Errorf("parse layout.html: %w", err)
	}
	return t, nil
}

// loadDocs walks the docs tree and decides what each file is published as.
func loadDocs(docsDir string) ([]docPage, error) {
	var pages []docPage

	err := filepath.WalkDir(docsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(docsDir, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		pages = append(pages, docPage{
			Source:  rel,
			Out:     pagePath(rel),
			Title:   titleOf(body, rel),
			Section: sectionOf(rel),
			Body:    body,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pages, nil
}

// pagePath maps a markdown file to its output path. README.md becomes
// index.html so that docs/adr/README.md is the directory's landing page.
func pagePath(rel string) string {
	rel = filepath.ToSlash(rel)
	dir, file := path.Split(rel)
	if strings.EqualFold(file, "README.md") {
		return "docs/" + dir + "index.html"
	}
	return "docs/" + strings.TrimSuffix(rel, ".md") + ".html"
}

func sectionOf(rel string) string {
	if strings.Contains(rel, "/") {
		return "Decisions"
	}
	return "Guides"
}

// titleOf takes the first level-one heading, which every document here has.
// Falling back to the file name keeps a missing heading from producing a blank
// nav entry.
func titleOf(body []byte, rel string) string {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(rest)
		}
	}
	base := path.Base(rel)
	return strings.TrimSuffix(strings.ReplaceAll(base, "-", " "), ".md")
}

// firstParagraph is the meta description: the first prose line of the
// document, with the markdown that usually decorates it removed.
func firstParagraph(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "|") || strings.HasPrefix(line, "```") ||
			strings.HasPrefix(line, "-") || strings.HasPrefix(line, ">") {
			continue
		}
		line = strings.ReplaceAll(line, "`", "")
		line = strings.ReplaceAll(line, "**", "")
		if len(line) > 160 {
			line = strings.TrimSpace(line[:157]) + "…"
		}
		return line
	}
	return "Documentation for lezz, the lab orchestrator."
}

func renderDoc(md goldmark.Markdown, docsDir string, p docPage, base string) (template.HTML, error) {
	doc := md.Parser().Parse(text.NewReader(p.Body))
	rewriteLinks(doc, path.Dir(p.Source), docsDir, base)

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, p.Body, doc); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func renderLanding(layout *template.Template, data siteData) (pageData, error) {
	raw, err := assets.ReadFile("landing.html")
	if err != nil {
		return pageData{}, err
	}
	content, err := template.New("landing").Parse(string(raw))
	if err != nil {
		return pageData{}, fmt.Errorf("parse landing.html: %w", err)
	}
	var buf bytes.Buffer
	page := pageData{
		siteData:    data,
		Title:       "lezz — the lab orchestrator",
		Description: "lezz installs and keeps the lab's tools up to date, brings a cluster up in one command, and measures whether a caller can still be trusted after it connects.",
		BodyClass:   "landing",
	}
	if err := content.Execute(&buf, page); err != nil {
		return pageData{}, fmt.Errorf("render landing.html: %w", err)
	}
	page.Content = template.HTML(buf.String())
	return page, nil
}

// rewriteLinks points markdown-to-markdown links at the HTML pages this build
// actually produced. Links that leave docs/ (the README, a kb/ path) or that
// are not markdown are left untouched rather than guessed at.
func rewriteLinks(doc ast.Node, srcDir, docsDir, base string) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		link, ok := n.(*ast.Link)
		if !ok {
			return ast.WalkContinue, nil
		}
		if dest, ok := resolveDocLink(string(link.Destination), srcDir, docsDir, base); ok {
			link.Destination = []byte(dest)
		}
		return ast.WalkContinue, nil
	})
}

func resolveDocLink(dest, srcDir, docsDir, base string) (string, bool) {
	if dest == "" || strings.Contains(dest, "://") ||
		strings.HasPrefix(dest, "#") || strings.HasPrefix(dest, "mailto:") {
		return "", false
	}

	target, frag := dest, ""
	if i := strings.Index(dest, "#"); i >= 0 {
		target, frag = dest[:i], dest[i:]
	}
	if !strings.HasSuffix(target, ".md") {
		return "", false
	}

	// Resolve relative to the document, the way the renderer's reader would.
	// The result is docs-root-relative, so the same computation works whether
	// the root was passed as a relative or an absolute path.
	rel := path.Clean(path.Join(srcDir, target))
	if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(docsDir, filepath.FromSlash(rel))); err != nil {
		return "", false
	}
	return base + pagePath(rel) + frag, true
}
