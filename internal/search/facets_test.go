package search

import (
	"reflect"
	"testing"
)

func TestParseFacets(t *testing.T) {
	cases := []struct {
		in       string
		wantText string
		wantF    Facets
	}{
		{"hello world", "hello world", Facets{}},
		{"tag:ai", "", Facets{Tags: []string{"ai"}}},
		{"llm tag:ai", "llm", Facets{Tags: []string{"ai"}}},
		{"-tag:draft react", "react", Facets{NotTags: []string{"draft"}}},
		{"kind:video tutorial", "tutorial", Facets{Kind: "video"}},
		{"tag:ai tag:llm bench", "bench", Facets{Tags: []string{"ai", "llm"}}},
		{"in:title react", "react", Facets{Scope: "title"}},
		{"in:url localhost", "localhost", Facets{Scope: "url"}},
		{"in:bogus token", "in:bogus token", Facets{}}, // unknown in: value falls through
		{"-foo react", "-foo react", Facets{}},         // dash-only token without colon → not a facet
		{": orphan", ": orphan", Facets{}},             // bare colon also passes through
		{"unknown:key keep", "unknown:key keep", Facets{}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			gotText, gotF := ParseFacets(tc.in)
			if gotText != tc.wantText {
				t.Errorf("text = %q, want %q", gotText, tc.wantText)
			}
			if !reflect.DeepEqual(gotF, tc.wantF) {
				t.Errorf("facets = %+v, want %+v", gotF, tc.wantF)
			}
		})
	}
}

func TestFacets_Empty(t *testing.T) {
	if !(Facets{}).Empty() {
		t.Errorf("zero Facets should be empty")
	}
	if (Facets{Tags: []string{"x"}}).Empty() {
		t.Errorf("Facets with tags should not be empty")
	}
	if (Facets{Kind: "video"}).Empty() {
		t.Errorf("Facets with kind should not be empty")
	}
}
