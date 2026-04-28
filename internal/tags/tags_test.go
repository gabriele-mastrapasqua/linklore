package tags

import (
	"reflect"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"  ":                  "",
		"!!":                  "",
		"Go":                  "go",
		"  Hello, World!  ":   "hello-world",
		"Machine Learning":    "machine-learning",
		"Artificial-Intelligence": "artificial-intelligence",
		"AI/ML":               "ai-ml",
		"Tags":                "tag",   // trailing 's' lemma
		"Bus":                 "bus",   // length ≤3 keeps the s
		"databases":           "database",
		"  multi   space  ":   "multi-space",
		"NLP":                 "nlp",
		"©™ Foo":              "foo",
		"3D":                  "3d",
		"AI ":                 "ai",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalize_dedupAndOrder(t *testing.T) {
	got := Normalize([]string{"Go", "go", "GO!", "rust"}, nil, Default())
	want := []string{"go", "rust"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNormalize_capPerLink(t *testing.T) {
	in := []string{"a", "b", "c", "d", "e", "f"}
	got := Normalize(in, nil, Config{MaxPerLink: 3})
	if len(got) != 3 {
		t.Errorf("len = %d", len(got))
	}
}

func TestNormalize_emptyInputsSkipped(t *testing.T) {
	got := Normalize([]string{"", "  ", "!!", "go"}, nil, Default())
	if !reflect.DeepEqual(got, []string{"go"}) {
		t.Errorf("got %v", got)
	}
}

func TestNormalize_fuzzyReuseSnapsToExisting(t *testing.T) {
	// Existing "machine-learning"; a new tag "machine-learnin" should snap.
	existing := []string{"machine-learning", "go", "rust"}
	got := Normalize([]string{"Machine Learnin"}, existing, Default())
	if len(got) != 1 || got[0] != "machine-learning" {
		t.Errorf("expected snap to existing, got %v", got)
	}
}

func TestNormalize_fuzzyDistanceBudget(t *testing.T) {
	// "go" → "rust" is far enough not to snap even with d=1
	existing := []string{"rust"}
	got := Normalize([]string{"go"}, existing, Config{MaxPerLink: 5, ReuseDistance: 1})
	if !reflect.DeepEqual(got, []string{"go"}) {
		t.Errorf("unexpected snap: %v", got)
	}
}

func TestNormalize_zeroDistanceNoFuzzy(t *testing.T) {
	existing := []string{"go-lang"}
	got := Normalize([]string{"golang"}, existing, Config{MaxPerLink: 5, ReuseDistance: 0})
	// With distance=0 we must NOT snap "golang"→"go-lang".
	if !reflect.DeepEqual(got, []string{"golang"}) {
		t.Errorf("got %v", got)
	}
}

func TestLevenshtein_earlyExit(t *testing.T) {
	if d := levenshtein("kitten", "sitting", 2); d != 3 {
		// distance is 3; budget 2 forces early exit ⇒ returns 3 (>budget)
		if d <= 2 {
			t.Errorf("expected >2, got %d", d)
		}
	}
	if d := levenshtein("foo", "foo", 5); d != 0 {
		t.Errorf("equal d = %d", d)
	}
	if d := levenshtein("a", "abcdefgh", 1); d <= 1 {
		t.Errorf("len-prune broken: %d", d)
	}
}
