package letterboxd

import (
	"reflect"
	"testing"
)

func TestExtractListPath(t *testing.T) {
	cases := map[string]string{
		"https://letterboxd.com/alexanderh/list/letterboxd-one-million-watched-club/": "alexanderh/list/letterboxd-one-million-watched-club",
		"letterboxd.com/alexanderh/list/letterboxd-one-million-watched-club":          "alexanderh/list/letterboxd-one-million-watched-club",
		"alexanderh/list/letterboxd-one-million-watched-club/":                        "alexanderh/list/letterboxd-one-million-watched-club",
		"not-a-list-url": "",
	}
	for in, want := range cases {
		if got := extractListPath(in); got != want {
			t.Errorf("extractListPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseFilmSlugs(t *testing.T) {
	in := `["/film/inception/","/film/fight-club/","/film/inception/"]`
	got := parseFilmSlugs(in)
	want := []string{"inception", "fight-club", "inception"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseFilmSlugs(%q) = %v, want %v", in, got, want)
	}
}
