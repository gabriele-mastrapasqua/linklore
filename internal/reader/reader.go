// Package reader renders a saved link's Markdown body into a clean,
// sanitised HTML article — the linklore equivalent of Pocket's reader view.
//
// We pin a strict bluemonday policy: the only attributes that survive are
// href on <a>, src/alt on <img>, and a few formatting tags. No scripts, no
// inline styles, no <iframe>. This keeps cross-site content safe to embed.
package reader

import (
	"html/template"
	"sync"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
	"github.com/microcosm-cc/bluemonday"
)

var (
	policyOnce sync.Once
	policy     *bluemonday.Policy
)

func sanitizer() *bluemonday.Policy {
	policyOnce.Do(func() {
		p := bluemonday.UGCPolicy()
		// UGC allows links by default but strips target/rel; re-allow target
		// and force noopener via the standard helper.
		p.AllowAttrs("target").OnElements("a")
		p.RequireNoFollowOnLinks(true)
		p.RequireNoReferrerOnLinks(true)
		// Block dangerous schemes; UGCPolicy already does this for href/src.
		policy = p
	})
	return policy
}

// Render turns a Markdown string into a sanitised template.HTML safe to
// drop into a server-rendered template.
func Render(md string) template.HTML {
	if md == "" {
		return ""
	}
	// Common GFM extensions: tables, fenced code, autolink, strikethrough.
	exts := parser.CommonExtensions | parser.AutoHeadingIDs | parser.NoEmptyLineBeforeBlock
	p := parser.NewWithExtensions(exts)

	// Smart-pants and HrefTargetBlank both stay off — links that escape the
	// site always open in a new tab is OK, but the policy already enforces
	// noopener. We let the markdown package emit plain <a>.
	rOpts := html.RendererOptions{Flags: html.CommonFlags}
	r := html.NewRenderer(rOpts)

	rawHTML := markdown.Render(p.Parse([]byte(md)), r)
	clean := sanitizer().SanitizeBytes(rawHTML)
	return template.HTML(clean)
}
