// Edge cases for the Netscape parser/writer.
package netscape

import (
	"bytes"
	"strings"
	"testing"
)

func TestParse_emptyDocument(t *testing.T) {
	bs, err := Parse(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 0 {
		t.Errorf("got %d bookmarks from empty input", len(bs))
	}
}

func TestParse_skipsAnchorsWithoutHref(t *testing.T) {
	doc := `<DL>
		<DT><A>no href</A>
		<DT><A HREF="">also no href</A>
		<DT><A HREF="https://good.example.com">good</A>
	</DL>`
	bs, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 1 || bs[0].URL != "https://good.example.com" {
		t.Errorf("expected only the well-formed anchor, got %v", bs)
	}
}

func TestParse_nestedFolderChain(t *testing.T) {
	doc := `<DL>
		<DT><H3>Top</H3>
		<DL>
			<DT><H3>Mid</H3>
			<DL>
				<DT><H3>Leaf</H3>
				<DL>
					<DT><A HREF="https://x.example.com">deep</A>
				</DL>
			</DL>
		</DL>
	</DL>`
	bs, _ := Parse(strings.NewReader(doc))
	if len(bs) != 1 {
		t.Fatalf("got %d", len(bs))
	}
	if bs[0].Folder != "Top/Mid/Leaf" {
		t.Errorf("folder = %q, want Top/Mid/Leaf", bs[0].Folder)
	}
}

func TestParse_malformedAddDateIgnored(t *testing.T) {
	doc := `<DL>
		<DT><A HREF="https://x.example.com" ADD_DATE="not-a-number">x</A>
	</DL>`
	bs, _ := Parse(strings.NewReader(doc))
	if len(bs) != 1 {
		t.Fatalf("got %d", len(bs))
	}
	if !bs[0].AddedAt.IsZero() {
		t.Errorf("malformed ADD_DATE should leave AddedAt zero, got %v", bs[0].AddedAt)
	}
}

func TestWrite_emptySliceProducesValidStub(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "<!DOCTYPE NETSCAPE-Bookmark-file-1>") {
		t.Errorf("missing doctype on empty export")
	}
	// Empty file should still be parseable.
	if bs, err := Parse(&buf); err != nil || len(bs) != 0 {
		t.Errorf("empty roundtrip: %d bookmarks, err=%v", len(bs), err)
	}
}

func TestWrite_escapesSpecialCharacters(t *testing.T) {
	var buf bytes.Buffer
	src := []WriteEntry{{
		URL:         "https://example.com/?a=1&b=2",
		Title:       `Tom & Jerry "deluxe" <2026>`,
		Description: "needs escaping",
		Folder:      "F&Q",
	}}
	if err := Write(&buf, src); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, raw := range []string{`"deluxe"`, `Tom & Jerry`, "<2026>"} {
		if strings.Contains(out, raw) {
			t.Errorf("unescaped substring %q found in output", raw)
		}
	}
	// And the roundtrip should still recover the original strings.
	rt, _ := Parse(&buf)
	if len(rt) != 1 || rt[0].Title != src[0].Title {
		t.Errorf("title roundtrip broken: got %q, want %q", rt[0].Title, src[0].Title)
	}
}

func TestParse_preservesTagsCommaSeparated(t *testing.T) {
	doc := `<DL>
		<DT><A HREF="https://x.example.com" TAGS="a, b ,c,,d ">x</A>
	</DL>`
	bs, _ := Parse(strings.NewReader(doc))
	if len(bs) != 1 {
		t.Fatalf("got %d", len(bs))
	}
	got := bs[0].Tags
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("tag[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
