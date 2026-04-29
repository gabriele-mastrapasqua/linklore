package classify

import "testing"

func TestFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://www.youtube.com/watch?v=abc", KindVideo},
		{"https://youtu.be/abc", KindVideo},
		{"https://vimeo.com/123", KindVideo},
		{"https://open.spotify.com/track/x", KindAudio},
		{"https://example.com/foo.pdf", KindDocument},
		{"https://example.com/foo.EPUB", KindBook},
		{"https://example.com/foo.mp4", KindVideo},
		{"https://example.com/foo.mp3", KindAudio},
		{"https://imgur.com/x", KindImage},
		{"https://example.com/blog/post", KindArticle},
		{"  https://www.YouTube.com/foo  ", KindVideo},
		{"not a url", KindArticle},
	}
	for _, c := range cases {
		if got := FromURL(c.url); got != c.want {
			t.Errorf("FromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestFromOGType(t *testing.T) {
	if got := FromOGType("video.movie", KindArticle); got != KindVideo {
		t.Errorf("video.movie → %q", got)
	}
	if got := FromOGType("music.song", KindArticle); got != KindAudio {
		t.Errorf("music.song → %q", got)
	}
	if got := FromOGType("", KindVideo); got != KindVideo {
		t.Errorf("empty fallback not respected: %q", got)
	}
}
