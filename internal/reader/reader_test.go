package reader

import (
	"strings"
	"testing"
)

func TestRender_basicMarkdown(t *testing.T) {
	out := string(Render("# Hello\n\nworld **bold** and [link](https://example.com)"))
	for _, want := range []string{"<h1", "Hello</h1>", "<strong>bold</strong>", `href="https://example.com"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRender_stripsScript(t *testing.T) {
	out := string(Render("# Title\n\n<script>alert('xss')</script>\n\nbody"))
	if strings.Contains(strings.ToLower(out), "<script") {
		t.Errorf("script tag survived sanitiser: %s", out)
	}
}

func TestRender_inlineEventHandlerStripped(t *testing.T) {
	out := string(Render(`<a href="https://x" onclick="alert(1)">click</a>`))
	if strings.Contains(strings.ToLower(out), "onclick") {
		t.Errorf("onclick survived: %s", out)
	}
}

func TestRender_javascriptHrefRejected(t *testing.T) {
	out := string(Render(`[bad](javascript:alert(1))`))
	if strings.Contains(strings.ToLower(out), "javascript:") {
		t.Errorf("javascript scheme survived: %s", out)
	}
}

func TestRender_relAddedToLinks(t *testing.T) {
	out := string(Render("[ext](https://example.com)"))
	if !strings.Contains(out, `rel="`) || !strings.Contains(out, "nofollow") {
		t.Errorf("expected rel attributes: %s", out)
	}
}

func TestRender_codeBlocks(t *testing.T) {
	out := string(Render("```go\nfunc f() {}\n```"))
	if !strings.Contains(out, "<code") {
		t.Errorf("code fence not rendered: %s", out)
	}
}

func TestRender_emptyInput(t *testing.T) {
	if Render("") != "" {
		t.Error("expected empty output")
	}
}

func TestRender_table(t *testing.T) {
	md := "| a | b |\n|---|---|\n| 1 | 2 |\n"
	out := string(Render(md))
	if !strings.Contains(out, "<table") {
		t.Errorf("table not rendered: %s", out)
	}
}
