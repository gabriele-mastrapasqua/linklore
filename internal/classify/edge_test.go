// Edge cases that aren't obvious from the small "happy path" table —
// captured here so the heuristics don't silently drift.
package classify

import "testing"

func TestFromURL_subdomainsAndPrefixes(t *testing.T) {
	cases := []struct {
		url, want string
	}{
		{"https://m.youtube.com/watch?v=abc", KindVideo},
		{"https://music.youtube.com/watch?v=abc", KindVideo},
		{"https://player.vimeo.com/video/123", KindVideo},
		{"https://api.soundcloud.com/tracks/123", KindAudio},
		{"https://i.imgur.com/foo.jpg", KindImage},
		{"https://www.imgur.com/gallery/foo", KindImage},
	}
	for _, c := range cases {
		if got := FromURL(c.url); got != c.want {
			t.Errorf("FromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestFromURL_extensionWinsOverHostFallback(t *testing.T) {
	// Some random article host that happens to link a PDF should
	// classify as document, not article.
	if got := FromURL("https://news.example.com/whitepaper.pdf"); got != KindDocument {
		t.Errorf("got %q, want %q", got, KindDocument)
	}
}

func TestFromURL_querystringDoesNotConfuse(t *testing.T) {
	// .pdf in the querystring shouldn't trigger document — only the
	// path extension counts.
	if got := FromURL("https://example.com/page?download=foo.pdf"); got != KindArticle {
		t.Errorf("got %q, want article (querystring extensions ignored)", got)
	}
}

func TestFromURL_uppercaseScheme(t *testing.T) {
	if got := FromURL("HTTPS://YOUTU.BE/abc"); got != KindVideo {
		t.Errorf("got %q for uppercase scheme/host, want video", got)
	}
}

func TestFromURL_emptyAndJunk(t *testing.T) {
	cases := []string{"", "  ", "javascript:void(0)", "://broken", "/relative/path"}
	for _, c := range cases {
		if got := FromURL(c); got != KindArticle {
			t.Errorf("FromURL(%q) = %q, want article (default)", c, got)
		}
	}
}

func TestFromOGType_unknownPreservesFallback(t *testing.T) {
	if got := FromOGType("totally.unknown", KindAudio); got != KindAudio {
		t.Errorf("unknown og:type should preserve fallback, got %q", got)
	}
	if got := FromOGType("ARTICLE", KindVideo); got != KindArticle {
		t.Errorf("case-insensitive match failed, got %q", got)
	}
}

func TestFromOGType_emptyTrimsToFallback(t *testing.T) {
	if got := FromOGType("   ", KindBook); got != KindBook {
		t.Errorf("whitespace-only og:type should fall back, got %q", got)
	}
}
