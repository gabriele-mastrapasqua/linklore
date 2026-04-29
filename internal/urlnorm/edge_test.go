// Edge cases for the URL normaliser. Each represents a real-world
// shape we want to canonicalise the same way to catch duplicates.
package urlnorm

import "testing"

func TestNormalize_schemeVariationsCollapse(t *testing.T) {
	a := Normalize("http://example.com/x")
	b := Normalize("https://example.com/x")
	c := Normalize("HTTPS://EXAMPLE.com/x")
	if a != b || b != c {
		t.Errorf("scheme/case variants didn't collapse: a=%q b=%q c=%q", a, b, c)
	}
}

func TestNormalize_portsArePreserved(t *testing.T) {
	// We DO keep ports — example.com:8080 is a different deployment
	// than example.com on 443 and shouldn't dedupe.
	a := Normalize("https://example.com:8080/x")
	b := Normalize("https://example.com/x")
	if a == b {
		t.Errorf("ports should differ: a=%q b=%q", a, b)
	}
}

func TestNormalize_dropsAllKnownTrackers(t *testing.T) {
	noisy := "https://example.com/path?utm_source=a&utm_medium=b&utm_campaign=c&fbclid=d&gclid=e&yclid=f&msclkid=g&dclid=h&igshid=i&ref=j&ref_src=k&mc_eid=l&_hsenc=m&_hsmi=n"
	clean := "https://example.com/path"
	if got, want := Normalize(noisy), Normalize(clean); got != want {
		t.Errorf("trackers not stripped: got %q, want %q", got, want)
	}
}

func TestNormalize_keepsLegitimateParams(t *testing.T) {
	a := Normalize("https://example.com/search?q=hello&page=2")
	if a != "example.com/search?page=2&q=hello" {
		t.Errorf("query stripped or reordered wrongly: %q", a)
	}
}

func TestNormalize_emptyPathStaysSlash(t *testing.T) {
	// Trailing-slash trimming must not eat a bare "/" path — that
	// would conflate the homepage with deep routes after stripping.
	if got := Normalize("https://example.com/"); got != "example.com/" {
		t.Errorf("bare slash path = %q, want example.com/", got)
	}
}

func TestNormalize_caseInsensitiveTrackerKeys(t *testing.T) {
	a := Normalize("https://example.com/?UTM_Source=x&FBCLID=y")
	b := Normalize("https://example.com/?utm_source=x&fbclid=y")
	if a != b {
		t.Errorf("case-insensitive tracker matching broken: a=%q b=%q", a, b)
	}
}

func TestNormalize_repeatedQueryKeysSorted(t *testing.T) {
	// Duplicate values for the same key are sorted so two URLs that
	// list them in different orders still collapse.
	a := Normalize("https://example.com/?tag=b&tag=a")
	b := Normalize("https://example.com/?tag=a&tag=b")
	if a != b {
		t.Errorf("repeated keys didn't collapse: a=%q b=%q", a, b)
	}
}

func TestNormalize_fragmentDropped(t *testing.T) {
	a := Normalize("https://example.com/page#section-1")
	b := Normalize("https://example.com/page#section-2")
	if a != b {
		t.Errorf("fragments not dropped: a=%q b=%q", a, b)
	}
}
