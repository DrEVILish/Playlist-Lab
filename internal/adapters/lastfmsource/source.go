// Package lastfmsource ports scrapeLastfmPlaylist (scrapers.ts:955) as a
// SourceAdapter, backing the "lastfm" chart-URL import (import.ts's
// handleImport 'lastfm' case) - NOT the same thing as the country-chart
// browsing charts.ts already handles for deezer/apple/spotify. Named
// lastfmsource (not lastfm) to avoid colliding with the already-ported
// internal/services/lastfm package it wraps, which this adapter is the only
// caller of chart.gettoptracks/gettopartists/gettoptags for.
package lastfmsource

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/services/lastfm"
)

const serviceName = "lastfm"

// ErrNoTracks mirrors the original throwing "Failed to fetch Last.fm chart"
// when a chart request comes back empty.
var ErrNoTracks = errors.New("failed to fetch Last.fm chart: no tracks returned")

type Source struct{}

func NewSource() *Source { return &Source{} }

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "Last.fm", Icon: "lastfm"}
}

// FetchTracks ports scrapeLastfmPlaylist's chart-type dispatch: the
// playlistURLOrID is a last.fm chart URL (or just "top-tracks" etc.), and
// which chart type to fetch is inferred from substrings in it, exactly as
// the original does.
func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	var (
		name   string
		tracks []lastfm.Track
	)

	switch {
	case strings.Contains(playlistURLOrID, "top-artists"):
		name = "Last.fm Top Artists"
		artists := lastfm.GetTopArtists(ctx, 50)
		if len(artists) > 20 {
			artists = artists[:20]
		}
		for _, a := range artists {
			top := lastfm.GetArtistTopTracks(ctx, a.Name, 5)
			if len(top) > 3 {
				top = top[:3]
			}
			for _, t := range top {
				tracks = append(tracks, lastfm.Track{Title: t.Title, Artist: a.Name})
			}
		}

	case strings.Contains(playlistURLOrID, "top-tags"):
		name = "Last.fm Top Tags"
		for _, tag := range lastfm.GetTopTags(ctx, 10) {
			tracks = append(tracks, lastfm.GetTagTopTracks(ctx, tag, 10)...)
		}

	case strings.Contains(playlistURLOrID, "/tag/"):
		tagName := extractTagName(playlistURLOrID)
		name = "Last.fm Top " + strings.ToUpper(tagName[:1]) + tagName[1:]
		tracks = lastfm.GetTagTopTracks(ctx, tagName, 100)

	default: // "top-tracks" and any unrecognized URL, matching the original's default branch
		name = "Last.fm Top Tracks"
		tracks = lastfm.GetTopTracks(ctx, 100)
	}

	if len(tracks) == 0 {
		return adapters.PlaylistInfo{}, nil, ErrNoTracks
	}

	out := make([]adapters.TrackInfo, len(tracks))
	for i, t := range tracks {
		out[i] = adapters.TrackInfo{Title: t.Title, Artist: t.Artist}
	}
	return adapters.PlaylistInfo{ID: "lastfm-chart", Name: name, TrackCount: len(out)}, out, nil
}

// extractTagName ports the /tag/(name) URL regex in scrapeLastfmPlaylist,
// falling back to "rock" like the original.
func extractTagName(rawURL string) string {
	idx := strings.Index(rawURL, "/tag/")
	if idx < 0 {
		return "rock"
	}
	rest := rawURL[idx+len("/tag/"):]
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		rest = rest[:slash]
	}
	if rest == "" {
		return "rock"
	}
	if decoded, err := url.QueryUnescape(rest); err == nil {
		return decoded
	}
	return rest
}
