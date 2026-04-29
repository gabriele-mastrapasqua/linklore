package netscape

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const sample = `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
    <DT><H3 ADD_DATE="1700000000">Toolbar</H3>
    <DL><p>
        <DT><A HREF="https://example.com" ADD_DATE="1700000001" TAGS="news,daily">Example</A>
        <DD>An example domain
        <DT><A HREF="https://golang.org">Go</A>
    </DL><p>
    <DT><A HREF="https://orphan.example.com">Orphan</A>
</DL><p>
`

func TestParse_extractsURLs(t *testing.T) {
	bs, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 3 {
		t.Fatalf("got %d, want 3", len(bs))
	}
	if bs[0].URL != "https://example.com" {
		t.Errorf("first url = %q", bs[0].URL)
	}
	if bs[0].Title != "Example" {
		t.Errorf("title = %q", bs[0].Title)
	}
	if bs[0].Description != "An example domain" {
		t.Errorf("description = %q", bs[0].Description)
	}
	if bs[0].Folder != "Toolbar" {
		t.Errorf("folder = %q", bs[0].Folder)
	}
	if got := bs[0].Tags; len(got) != 2 || got[0] != "news" || got[1] != "daily" {
		t.Errorf("tags = %v", got)
	}
	if bs[0].AddedAt.Unix() != 1700000001 {
		t.Errorf("added_at = %v", bs[0].AddedAt)
	}
	if bs[2].URL != "https://orphan.example.com" {
		t.Errorf("orphan url = %q", bs[2].URL)
	}
	if bs[2].Folder != "" {
		t.Errorf("orphan folder = %q (want empty)", bs[2].Folder)
	}
}

func TestWriteRoundtrip(t *testing.T) {
	src := []WriteEntry{
		{URL: "https://a.example", Title: "A", Folder: "Folder", AddedAt: time.Unix(1700000000, 0)},
		{URL: "https://b.example", Title: "B", Folder: "Folder", Tags: []string{"x", "y"}},
		{URL: "https://c.example", Title: "C", Description: "third"},
	}
	var buf bytes.Buffer
	if err := Write(&buf, src); err != nil {
		t.Fatal(err)
	}
	out, err := Parse(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("roundtrip len = %d", len(out))
	}
	if out[0].URL != "https://a.example" || out[0].Folder != "Folder" {
		t.Errorf("first: %+v", out[0])
	}
	if got := out[1].Tags; len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("tags after roundtrip: %v", got)
	}
	if out[2].Description != "third" {
		t.Errorf("description = %q", out[2].Description)
	}
}
