package youtubemusic

import "testing"

// realBrowsePlaylistResponse is a trimmed-but-structurally-faithful copy of a
// real anonymous POST /youtubei/v1/browse response for a public YouTube
// Music playlist (browseId "VL"+playlistId, no cookie) - captured live to
// pin down extractPlaylistTracks' container path
// (twoColumnBrowseResultsRenderer.secondaryContents..., not the
// singleColumnBrowseResultsRenderer/tabbedSearchResultsRenderer shapes the
// browse-home feed and search responses use) against the real shape rather
// than a guess.
func realBrowsePlaylistResponse() map[string]any {
	item := func(videoID, title, artist string) any {
		return map[string]any{
			"musicResponsiveListItemRenderer": map[string]any{
				"playlistItemData": map[string]any{"videoId": videoID},
				"flexColumns": []any{
					map[string]any{
						"musicResponsiveListItemFlexColumnRenderer": map[string]any{
							"text": map[string]any{"runs": []any{map[string]any{"text": title}}},
						},
					},
					map[string]any{
						"musicResponsiveListItemFlexColumnRenderer": map[string]any{
							"text": map[string]any{"runs": []any{map[string]any{"text": artist}}},
						},
					},
				},
			},
		}
	}
	return map[string]any{
		"contents": map[string]any{
			"twoColumnBrowseResultsRenderer": map[string]any{
				"secondaryContents": map[string]any{
					"sectionListRenderer": map[string]any{
						"contents": []any{
							map[string]any{
								"musicPlaylistShelfRenderer": map[string]any{
									"playlistId": "RDCLAK5uy_k8QSlZPzi4R3781ftLrAKefTJJ6x7JrVA",
									"contents": []any{
										item("CduA0TULnow", "Oops!...I Did It Again (Official HD Video)", "Britney Spears"),
										item("dQw4w9WgXcQ", "Never Gonna Give You Up", "Rick Astley"),
									},
								},
							},
						},
					},
				},
			},
		},
		"microformat": map[string]any{
			"microformatDataRenderer": map[string]any{
				"title": "Pop's Biggest Hits",
				"thumbnail": map[string]any{
					"thumbnails": []any{map[string]any{"url": "https://yt3.googleusercontent.com/cover.jpg"}},
				},
			},
		},
	}
}

func TestExtractPlaylistTracks(t *testing.T) {
	tracks := extractPlaylistTracks(realBrowsePlaylistResponse())
	if len(tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(tracks))
	}
	if tracks[0].VideoID != "CduA0TULnow" || tracks[0].Title != "Oops!...I Did It Again (Official HD Video)" || tracks[0].Artist != "Britney Spears" {
		t.Errorf("unexpected first track: %+v", tracks[0])
	}
	if tracks[1].VideoID != "dQw4w9WgXcQ" || tracks[1].Artist != "Rick Astley" {
		t.Errorf("unexpected second track: %+v", tracks[1])
	}
}

func TestExtractPlaylistTracks_WrongContainerShape(t *testing.T) {
	// A search-response-shaped (tabbedSearchResultsRenderer) or browse-home-
	// shaped (singleColumnBrowseResultsRenderer) payload must not be
	// mistaken for a playlist page - extractPlaylistTracks should just find
	// nothing rather than panic on the mismatched shape.
	if tracks := extractPlaylistTracks(map[string]any{"contents": map[string]any{"singleColumnBrowseResultsRenderer": map[string]any{}}}); len(tracks) != 0 {
		t.Errorf("expected no tracks from a mismatched container shape, got %d", len(tracks))
	}
}

func TestYtmPlaylistIDPattern(t *testing.T) {
	cases := map[string]string{
		"https://music.youtube.com/playlist?list=PLabc123":   "PLabc123",
		"https://music.youtube.com/watch?v=x&list=RDxyz_9-Z": "RDxyz_9-Z",
		"not a url": "",
	}
	for in, want := range cases {
		m := ytmPlaylistIDPattern.FindStringSubmatch(in)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("ytmPlaylistIDPattern(%q) = %q, want %q", in, got, want)
		}
	}
}
