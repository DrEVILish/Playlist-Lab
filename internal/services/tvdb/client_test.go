package tvdb

import "testing"

func TestExtractListID(t *testing.T) {
	cases := map[string]string{
		"12345": "12345",
		"https://thetvdb.com/lists/12345-my-list": "12345",
		"thetvdb.com/lists/12345":                 "12345",
		"not-a-list":                              "",
	}
	for in, want := range cases {
		if got := extractListID(in); got != want {
			t.Errorf("extractListID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeListResponse(t *testing.T) {
	raw := []byte(`{"data":{"entities":[
		{"movieId":123,"name":"A Movie","year":"2020"},
		{"seriesId":456,"name":"A Series","year":"2019"},
		{"movieId":789}
	]}}`)

	movies, err := decodeListResponse(raw, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 2 {
		t.Fatalf("expected 2 movies, got %d: %+v", len(movies), movies)
	}
	if movies[0].GuidKey != "tvdb://123" || movies[0].Title != "A Movie" || movies[0].Year != 2020 {
		t.Errorf("unexpected first movie: %+v", movies[0])
	}
	if movies[1].GuidKey != "tvdb://789" || movies[1].Title != "tvdb://789" {
		t.Errorf("expected fallback title for nameless entity, got: %+v", movies[1])
	}

	series, err := decodeListResponse(raw, "tv")
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].GuidKey != "tvdb://456" {
		t.Errorf("unexpected series result: %+v", series)
	}
}
