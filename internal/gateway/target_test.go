package gateway

import "testing"

func TestParseDAVTargetRoot(t *testing.T) {
	target, err := parseDAVTarget("/")
	if err != nil {
		t.Fatal(err)
	}
	if !target.Root || !target.Collection {
		t.Fatalf("target = %#v", target)
	}
}

func TestParseDAVTargetCollection(t *testing.T) {
	target, err := parseDAVTarget("/photos/a/b%20c/")
	if err != nil {
		t.Fatal(err)
	}
	if target.Root || target.Bucket != "photos" || target.Key != "a/b c/" || !target.Collection {
		t.Fatalf("target = %#v", target)
	}
}

func TestParseDAVTargetObject(t *testing.T) {
	target, err := parseDAVTarget("/photos/a/b%20c.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if target.Root || target.Bucket != "photos" || target.Key != "a/b c.jpg" || target.Collection {
		t.Fatalf("target = %#v", target)
	}
}

func TestHrefForEscapesSegments(t *testing.T) {
	got := hrefFor("photos", "a/b c/#1.txt", false)
	want := "/photos/a/b%20c/%231.txt"
	if got != want {
		t.Fatalf("href = %q, want %q", got, want)
	}
}

func TestUpstreamPath(t *testing.T) {
	got := UpstreamPath("real photos", "a/b c/#1.txt")
	want := "/real%20photos/a/b%20c/%231.txt"
	if got != want {
		t.Fatalf("upstream path = %q, want %q", got, want)
	}
}
