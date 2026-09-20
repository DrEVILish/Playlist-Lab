package tmdb

import "testing"

func TestExtractListID(t *testing.T) {
	cases := map[string]string{
		"12345":   "12345",
		" 12345 ": "12345",
		"https://www.themoviedb.org/list/8493-a-list": "8493",
		"themoviedb.org/list/8493":                    "8493",
		"not-a-list":                                  "",
	}
	for in, want := range cases {
		if got := extractListID(in); got != want {
			t.Errorf("extractListID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateKeyRequiresKey(t *testing.T) {
	if err := NewClient("").ValidateKey(); err == nil {
		t.Error("expected error for empty API key")
	}
}

func TestDecodeCollectionSearchResponse(t *testing.T) {
	raw := []byte(`{"results":[{"id":1241,"name":"Harry Potter Collection"},{"id":10,"name":"Star Wars Collection"}]}`)
	results, err := decodeCollectionSearchResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].ID != 1241 || results[0].Name != "Harry Potter Collection" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestGetChartValidation(t *testing.T) {
	c := NewClient("dummy")
	if _, err := c.GetChart("movie", "trending", 10); err == nil {
		t.Error("expected error for unsupported chart")
	}
	if _, err := c.GetChart("album", "popular", 10); err == nil {
		t.Error("expected error for unsupported media type")
	}
}
