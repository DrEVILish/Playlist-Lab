package handlers

import (
	"testing"

	"github.com/drevilish/playlist-lab/internal/adapters"
)

func TestImportSourceFromLink(t *testing.T) {
	reg := adapters.NewRegistry()
	for _, id := range []string{"spotify", "deezer", "apple"} {
		reg.RegisterSource(&fakeSource{name: id})
	}
	h := &ImportHandler{Registry: reg}
	cases := []struct{ picked, identifier, want string }{
		{"spotify", "https://www.deezer.com/en/playlist/123", "deezer"},
		{"deezer", "https://open.spotify.com/playlist/abc?si=x", "spotify"},
		{"spotify", "https://music.apple.com/us/playlist/x/pl.1", "apple"},
		{"spotify", "37i9dQZF1DXcBWIGoYBM5M", "spotify"},            // bare ID keeps the picked source
		{"deezer", "https://tidal.com/browse/playlist/1", "deezer"}, // no tidal registered here
		{"spotify", "https://notdeezer.com/x", "spotify"},           // suffix match needs a dot boundary
	}
	for _, c := range cases {
		if got := h.importSource(c.picked, c.identifier); got != c.want {
			t.Errorf("importSource(%q, %q) = %q, want %q", c.picked, c.identifier, got, c.want)
		}
	}
}
