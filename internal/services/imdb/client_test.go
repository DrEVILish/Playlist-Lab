package imdb

import "testing"

func TestExtractListID(t *testing.T) {
	cases := map[string]string{
		"ls000873904":                                   "ls000873904",
		" ls000873904 ":                                 "ls000873904",
		"https://www.imdb.com/list/ls000873904/":        "ls000873904",
		"https://www.imdb.com/list/ls000873904":         "ls000873904",
		"www.imdb.com/list/ls000873904/?ref_=something": "ls000873904",
		"not-a-list":                                    "",
	}
	for in, want := range cases {
		if got := extractListID(in); got != want {
			t.Errorf("extractListID(%q) = %q, want %q", in, got, want)
		}
	}
}
