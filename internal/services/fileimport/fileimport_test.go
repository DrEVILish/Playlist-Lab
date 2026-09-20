package fileimport

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		content  string
		wantErr  bool
		want     []Track
	}{
		{
			name:     "m3u standard artist-title",
			filename: "playlist.m3u",
			content: "#EXTM3U\n" +
				"#EXTINF:200,Radiohead - Karma Police\n" +
				"radiohead - karma police.mp3\n" +
				"#EXTINF:180,Radiohead - Paranoid Android\n" +
				"radiohead - paranoid android.mp3\n",
			want: []Track{
				{Title: "Karma Police", Artist: "Radiohead"},
				{Title: "Paranoid Android", Artist: "Radiohead"},
			},
		},
		{
			name:     "m3u apple format title-artist detected from parens",
			filename: "playlist.m3u",
			content: "#EXTM3U\n" +
				"#EXTINF:200,Karma Police (Remastered) - Radiohead\n" +
				"track1.mp3\n" +
				"#EXTINF:180,Paranoid Android (Live) - Radiohead\n" +
				"track2.mp3\n",
			want: []Track{
				{Title: "Karma Police (Remastered)", Artist: "Radiohead"},
				{Title: "Paranoid Android (Live)", Artist: "Radiohead"},
			},
		},
		{
			name:     "m3u no extinf falls back to filename",
			filename: "playlist.m3u",
			content:  "#EXTM3U\nRadiohead - Karma Police.mp3\n",
			want:     []Track{{Title: "Karma Police", Artist: "Radiohead"}},
		},
		{
			name:     "m3u empty errors",
			filename: "playlist.m3u",
			content:  "#EXTM3U\n",
			wantErr:  true,
		},
		{
			name:     "txt extension uses m3u parser",
			filename: "playlist.txt",
			content:  "#EXTINF:0,Artist - Title\nfile.mp3\n",
			want:     []Track{{Title: "Title", Artist: "Artist"}},
		},
		{
			name:     "csv with header",
			filename: "playlist.csv",
			content:  "Track,Artist,Album,Duration,File Path\nKarma Police,Radiohead,OK Computer,4:21,\n",
			want:     []Track{{Title: "Karma Police", Artist: "Radiohead", Album: "OK Computer"}},
		},
		{
			name:     "csv headerless falls back to position",
			filename: "playlist.csv",
			content:  "Karma Police,Radiohead\n",
			want:     []Track{{Title: "Karma Police", Artist: "Radiohead"}},
		},
		{
			name:     "pls ordered by index",
			filename: "playlist.pls",
			content:  "[playlist]\nTitle2=Radiohead - Paranoid Android\nTitle1=Radiohead - Karma Police\nNumberOfEntries=2\n",
			want: []Track{
				{Title: "Karma Police", Artist: "Radiohead"},
				{Title: "Paranoid Android", Artist: "Radiohead"},
			},
		},
		{
			name:     "xspf",
			filename: "playlist.xspf",
			content: `<playlist><trackList>` +
				`<track><title>Karma Police</title><creator>Radiohead</creator><album>OK Computer</album></track>` +
				`</trackList></playlist>`,
			want: []Track{{Title: "Karma Police", Artist: "Radiohead", Album: "OK Computer"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Parse(tc.content, tc.filename)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got tracks: %+v", result.Tracks)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result.Tracks) != len(tc.want) {
				t.Fatalf("got %d tracks, want %d: %+v", len(result.Tracks), len(tc.want), result.Tracks)
			}
			for i, want := range tc.want {
				got := result.Tracks[i]
				if got.Title != want.Title || got.Artist != want.Artist || got.Album != want.Album {
					t.Errorf("track %d: got %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

func TestIsAllowedExtension(t *testing.T) {
	for _, ok := range []string{"a.m3u", "a.M3U8", "a.pls", "a.xspf", "a.csv", "a.txt"} {
		if !IsAllowedExtension(ok) {
			t.Errorf("expected %q to be allowed", ok)
		}
	}
	for _, bad := range []string{"a.mp3", "a", "a.json"} {
		if IsAllowedExtension(bad) {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}
