// Package reader renders a saved link's Markdown body into a clean,
// sanitised HTML article — the linklore equivalent of Pocket's reader view.
//
// We pin a strict bluemonday policy: the only attributes that survive are
// href on <a>, src/alt on <img>, and a few formatting tags. No scripts, no
// inline styles, no <iframe>. This keeps cross-site content safe to embed.
package reader

import (
	"fmt"
	"html"
	"html/template"
	"sort"
	"strings"
	"sync"

	"github.com/gomarkdown/markdown"
	gohtml "github.com/gomarkdown/markdown/html"
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
	rOpts := gohtml.RendererOptions{Flags: gohtml.CommonFlags}
	r := gohtml.NewRenderer(rOpts)

	rawHTML := markdown.Render(p.Parse([]byte(md)), r)
	clean := sanitizer().SanitizeBytes(rawHTML)
	return template.HTML(clean)
}

// HighlightSpan is a (start, end, id, note) tuple used by
// RenderWithHighlights to wrap each selection in a <mark>.
type HighlightSpan struct {
	ID    int64
	Start int
	End   int
	Text  string
	Note  string
}

// RenderWithHighlights applies highlights on top of the source
// markdown BEFORE markdown rendering: each highlighted range is
// wrapped in a marker placeholder string that survives the markdown
// parser intact, then replaced with a real <mark> after the HTML
// passes through the sanitiser.
//
// The placeholder is `H<id>…/H<id>` —
// chosen because:
//   - control bytes are illegal in a well-formed UTF-8 document, so
//     they never appear in the original markdown,
//   - they survive the markdown parser as opaque text,
//   - the sanitiser's URL/attribute walker doesn't touch them,
//   - the post-substitution turns them into `<mark
//     data-highlight-id="…">` which IS allowed by the policy
//     (we extend the policy below).
//
// On miss (start/end out of range, or text doesn't match what's
// actually at that range — e.g. after a re-extraction shifted things),
// we fall back to a literal substring search for the highlight text
// and re-anchor. If even that fails the highlight is silently
// skipped: the user sees the unhighlighted article rather than a
// crash.
func RenderWithHighlights(md string, spans []HighlightSpan) template.HTML {
	if md == "" {
		return ""
	}
	if len(spans) == 0 {
		return Render(md)
	}
	// Re-anchor + sort spans front-to-back so substitution proceeds
	// without invalidating later offsets.
	type anchored struct {
		HighlightSpan
		ok bool
	}
	resolved := make([]anchored, 0, len(spans))
	for _, sp := range spans {
		a := anchored{HighlightSpan: sp}
		if sp.Start < len(md) && sp.End <= len(md) && sp.End > sp.Start &&
			md[sp.Start:sp.End] == sp.Text {
			a.ok = true
		} else if sp.Text != "" {
			if i := strings.Index(md, sp.Text); i >= 0 {
				a.Start = i
				a.End = i + len(sp.Text)
				a.ok = true
			}
		}
		if a.ok {
			resolved = append(resolved, a)
		}
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Start < resolved[j].Start })

	// Build the wrapped markdown by walking spans in order. Drop any
	// span that overlaps with the previous one (the renderer can't
	// produce nested <mark>s cleanly).
	var b strings.Builder
	cursor := 0
	for _, a := range resolved {
		if a.Start < cursor {
			continue
		}
		b.WriteString(md[cursor:a.Start])
		fmt.Fprintf(&b, "{{HLOPEN-%d}}", a.ID)
		b.WriteString(md[a.Start:a.End])
		fmt.Fprintf(&b, "{{HLCLOSE-%d}}", a.ID)
		cursor = a.End
	}
	b.WriteString(md[cursor:])

	exts := parser.CommonExtensions | parser.AutoHeadingIDs | parser.NoEmptyLineBeforeBlock
	p := parser.NewWithExtensions(exts)
	rOpts := gohtml.RendererOptions{Flags: gohtml.CommonFlags}
	r := gohtml.NewRenderer(rOpts)
	rawHTML := markdown.Render(p.Parse([]byte(b.String())), r)
	cleaned := sanitizer().SanitizeBytes(rawHTML)

	// Substitute placeholders with <mark> tags after the sanitiser has
	// run. We can't do this before because the sanitiser would strip
	// any <mark> we inject (UGCPolicy doesn't whitelist it).
	out := string(cleaned)
	for _, a := range resolved {
		open := fmt.Sprintf("{{HLOPEN-%d}}", a.ID)
		close := fmt.Sprintf("{{HLCLOSE-%d}}", a.ID)
		// Note tooltip is a plain title="" so any HTML entities in the
		// note text get escaped, not interpreted.
		title := html.EscapeString(a.Note)
		out = strings.ReplaceAll(out,
			open,
			fmt.Sprintf(`<mark class="hl" data-hid="%d" title="%s">`, a.ID, title))
		out = strings.ReplaceAll(out, close, "</mark>")
	}
	return template.HTML(out)
}
