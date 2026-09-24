package shared

import (
	"context"
	"log/slog"
	"sort"

	"github.com/drevilish/playlist-lab/internal/adapters"
)

// MatchTracksBySearch is the ~25-line skeleton every catalog-search-based
// TargetAdapter.MatchTracks reimplemented independently: for each source
// track, check cancellation, search the target service's catalog, score
// and sort whatever came back, keep the best if it clears threshold, and
// report progress - identical control flow across Deezer, Spotify, Apple
// Music, YouTube Music, Qobuz, Tidal, and ListenBrainz, differing only in
// how each searches, scores, and builds a MatchResult. Collapsing it here
// means a behavior change (cancellation timing, tie-breaking, progress
// semantics) is one edit instead of seven.
//
// YouTube's own MatchTracks deliberately doesn't use this: it runs a
// stricter, differently-weighted scoring algorithm (see its own comment),
// not a drop-in of this skeleton.
//
// afterEach, if non-nil, runs after each track (progress included) except
// the last - for ListenBrainz's MusicBrainz rate limit, the one adapter
// that needs to pace requests rather than fire them back to back. Every
// other adapter passes nil.
func MatchTracksBySearch[T any](
	ctx context.Context,
	tracks []adapters.TrackInfo,
	progress func(current, total int),
	isCancelled func() bool,
	serviceName string,
	threshold float64,
	search func(ctx context.Context, track adapters.TrackInfo) ([]T, error),
	score func(sourceTitle, sourceArtist string, candidate T) float64,
	toMatchResult func(source adapters.TrackInfo, best T, score float64, matched bool) adapters.MatchResult,
	afterEach func(),
) ([]adapters.MatchResult, error) {
	results := make([]adapters.MatchResult, 0, len(tracks))
	for i, track := range tracks {
		if isCancelled != nil && isCancelled() {
			break
		}
		result := adapters.MatchResult{SourceTrack: track}

		items, err := search(ctx, track)
		if err != nil {
			slog.Warn(serviceName+" search failed for track", "track", track, "error", err)
		} else if len(items) > 0 {
			sort.SliceStable(items, func(a, b int) bool {
				return score(track.Title, track.Artist, items[a]) > score(track.Title, track.Artist, items[b])
			})
			best := items[0]
			s := score(track.Title, track.Artist, best)
			result = toMatchResult(track, best, s, s >= threshold)
		}

		results = append(results, result)
		if progress != nil {
			progress(i+1, len(tracks))
		}
		if afterEach != nil && i < len(tracks)-1 {
			afterEach()
		}
	}
	return results, nil
}
