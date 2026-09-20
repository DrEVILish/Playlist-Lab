package trakt

import "testing"

func TestDecodeSearchListsResponse(t *testing.T) {
	raw := []byte(`[
		{"type":"list","list":{"name":"1970-2021 Oscars Best Picture Winners","description":"Every winner","item_count":52,"likes":10,"ids":{"trakt":1,"slug":"1970-2021-oscars-best-picture-winners"},"user":{"ids":{"slug":"pjcob"}}}},
		{"type":"list","list":{"name":"No user slug","ids":{"slug":"x"},"user":{"ids":{"slug":""}}}}
	]`)
	results, err := decodeSearchListsResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (the one missing a user slug should be skipped), got %d: %+v", len(results), results)
	}
	want := ListSearchResult{
		Name: "1970-2021 Oscars Best Picture Winners", Description: "Every winner", ItemCount: 52, Likes: 10,
		URL: "https://trakt.tv/users/pjcob/lists/1970-2021-oscars-best-picture-winners",
	}
	if results[0] != want {
		t.Errorf("got %+v, want %+v", results[0], want)
	}
}

func TestExtractUserList(t *testing.T) {
	cases := []struct {
		in, wantUser, wantList string
		wantErr                bool
	}{
		{"https://trakt.tv/users/pjcob/lists/1970-2021-oscars-best-picture-winners?sort=rank,asc", "pjcob", "1970-2021-oscars-best-picture-winners", false},
		{"https://trakt.tv/users/pjcob/lists/2020-oscars", "pjcob", "2020-oscars", false},
		{"trakt.tv/users/pjcob/lists/2020-oscars", "pjcob", "2020-oscars", false},
		{"not-a-trakt-url", "", "", true},
	}
	for _, c := range cases {
		user, list, err := extractUserList(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("extractUserList(%q): expected error, got user=%q list=%q", c.in, user, list)
			}
			continue
		}
		if err != nil {
			t.Errorf("extractUserList(%q): unexpected error: %v", c.in, err)
			continue
		}
		if user != c.wantUser || list != c.wantList {
			t.Errorf("extractUserList(%q) = (%q, %q), want (%q, %q)", c.in, user, list, c.wantUser, c.wantList)
		}
	}
}

func TestDecodeListResponse(t *testing.T) {
	raw := []byte(`[
		{"type":"movie","movie":{"title":"Forrest Gump","year":1994,"ids":{"trakt":1,"slug":"forrest-gump-1994","imdb":"tt0109830","tmdb":13}}},
		{"type":"show","show":{"title":"Breaking Bad","year":2008,"ids":{"trakt":2,"slug":"breaking-bad","imdb":"tt0903747","tmdb":1396}}},
		{"type":"movie","movie":{"title":"No TMDb Match","year":1999,"ids":{"trakt":3,"slug":"x","imdb":"tt0000001","tmdb":0}}}
	]`)

	movies, err := decodeListResponse(raw, "movie")
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 2 {
		t.Fatalf("expected 2 movies, got %d: %+v", len(movies), movies)
	}
	if movies[0].GuidKey != "tmdb://13" || movies[0].Title != "Forrest Gump" || movies[0].Year != 1994 {
		t.Errorf("unexpected first movie: %+v", movies[0])
	}
	if movies[1].GuidKey != "imdb://tt0000001" {
		t.Errorf("expected IMDb fallback guid for a TMDb-less entry, got: %+v", movies[1])
	}

	shows, err := decodeListResponse(raw, "tv")
	if err != nil {
		t.Fatal(err)
	}
	if len(shows) != 1 || shows[0].GuidKey != "tmdb://1396" {
		t.Errorf("unexpected show result: %+v", shows)
	}
}
