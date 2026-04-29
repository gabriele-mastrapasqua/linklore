package urlnorm

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://www.example.com/foo/", "example.com/foo"},
		{"http://EXAMPLE.com/Foo/?utm_source=x&y=1#frag", "example.com/Foo?y=1"},
		{"https://example.com/?fbclid=abc&gclid=def", "example.com/"},
		{"https://example.com/?b=2&a=1", "example.com/?a=1&b=2"},
		{"https://example.com/path?utm_medium=x&utm_source=y", "example.com/path"},
		{"", ""},
		{"not-a-url", "not-a-url"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalize_collapsesEquivalents(t *testing.T) {
	a := Normalize("https://www.example.com/foo")
	b := Normalize("http://example.com/foo/?utm_source=ref")
	c := Normalize("https://example.com/foo#section")
	if a != b || b != c {
		t.Errorf("expected collapse, got a=%q b=%q c=%q", a, b, c)
	}
}
