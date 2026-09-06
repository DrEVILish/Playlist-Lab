// Package mixes ports services/mixes.ts's MixService: the mix-generation
// algorithms (Weekly/Daily/Time Capsule/New Music/Custom/Sonic/Deep
// Cuts/Artist Discovery/Mood/Era/Genre Evolution/Artist Journey/Workout/
// Forgotten Favorites/Genre Blend). Every method takes a server URL + Plex
// token + library id and returns a MixResult of track rating keys - the
// consuming handler is responsible for turning that into a Plex playlist.
//
// The TS version batches independent per-artist/per-seed Plex lookups with
// mapBatched to bound in-flight requests; this port keeps the same shape but
// with a plain bounded worker loop (batchSize goroutines at a time) since Go
// doesn't need a promise-pool helper for that.
package mixes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"sync"

	"github.com/drevilish/playlist-lab/internal/services/lastfm"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// plexLookupBatchSize mirrors plex.ts's PLEX_LOOKUP_BATCH_SIZE.
const plexLookupBatchSize = 8

// MixResult is the output of every generate* method: the ordered set of
// track rating keys to place in the resulting playlist.
type MixResult struct {
	TrackKeys  []string
	TrackCount int
}

func result(keys []string) MixResult { return MixResult{TrackKeys: keys, TrackCount: len(keys)} }

// ProgressFunc reports {stage, message, progress 0-100} updates during
// generateCustomMix, matching the TS progressEmitter.emit('progress', ...)
// calls. Callers that don't need progress reporting (e.g. non-interactive
// mix types) pass nil.
type ProgressFunc func(stage, message string, progress int)

func emit(fn ProgressFunc, stage, message string, progress int) {
	if fn != nil {
		fn(stage, message, progress)
	}
}

// Service groups every mix-generation algorithm. It's stateless -
// constructed fresh per call site, same as `new MixService()` in the TS
// routes.
type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) client(serverURL, plexToken string) *plex.Client {
	// clientID/product are only used for building playlist URIs, which
	// mix generation never does itself (the caller does, using its own
	// server-client-id) - empty strings here match plex.ts's mix-service
	// PlexClient construction, which also never builds URIs.
	return plex.NewClient(serverURL, plexToken, "", "Playlist Lab")
}

func shuffle[T any](in []T) []T {
	out := make([]T, len(in))
	copy(out, in)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// mapBatched runs fn over items with at most batchSize concurrent calls,
// preserving input order in the returned slice - matches plex.ts's
// mapBatched helper.
func mapBatched[T, R any](items []T, batchSize int, fn func(T) R) []R {
	out := make([]R, len(items))
	for start := 0; start < len(items); start += batchSize {
		end := start + batchSize
		if end > len(items) {
			end = len(items)
		}
		var wg sync.WaitGroup
		for i := start; i < end; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				out[i] = fn(items[i])
			}(i)
		}
		wg.Wait()
	}
	return out
}

// ---- Weekly Mix ----

type WeeklyMixSettings struct {
	TopArtists      int
	TracksPerArtist int
}

func (s *Service) GenerateWeeklyMix(serverURL, plexToken, libraryID string, settings WeeklyMixSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)

	recentTracks, err := p.GetRecentTracks(libraryID, 7, 0)
	if err != nil {
		return MixResult{}, err
	}
	if len(recentTracks) < 20 {
		recentTracks, err = p.GetRecentTracks(libraryID, 30, 0)
		if err != nil {
			return MixResult{}, err
		}
	}

	artistCounts := map[string]int{}
	var artistOrder []string
	for _, t := range recentTracks {
		artist := t.GrandparentTitle
		if artist == "" {
			artist = "Unknown"
		}
		if artist == "Various Artists" || artist == "Unknown" || artist == "Soundtrack" {
			continue
		}
		if _, ok := artistCounts[artist]; !ok {
			artistOrder = append(artistOrder, artist)
		}
		artistCounts[artist]++
	}

	sort.SliceStable(artistOrder, func(i, j int) bool { return artistCounts[artistOrder[i]] > artistCounts[artistOrder[j]] })
	if len(artistOrder) > settings.TopArtists {
		artistOrder = artistOrder[:settings.TopArtists]
	}
	if len(artistOrder) == 0 {
		return result(nil), nil
	}

	type lookup struct {
		tracks []plex.Track
		err    error
	}
	perArtist := mapBatched(artistOrder, plexLookupBatchSize, func(name string) lookup {
		artist, err := p.SearchArtist(libraryID, name)
		if err != nil || artist == nil {
			return lookup{nil, err}
		}
		tracks, err := p.GetArtistPopularTracks(libraryID, artist.RatingKey, settings.TracksPerArtist)
		return lookup{tracks, err}
	})

	var allTracks []string
	seen := map[string]bool{}
	for _, l := range perArtist {
		for _, t := range l.tracks {
			if !seen[t.RatingKey] {
				allTracks = append(allTracks, t.RatingKey)
				seen[t.RatingKey] = true
			}
		}
	}

	return result(shuffle(allTracks)), nil
}

// ---- Daily Mix ----

type DailyMixSettings struct {
	RecentTracks      int
	RelatedTracks     int
	RediscoveryTracks int
	RediscoveryDays   int
}

func (s *Service) GenerateDailyMix(serverURL, plexToken, libraryID string, settings DailyMixSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	seen := map[string]bool{}
	var mixTracks []string

	recentTracks, err := p.GetRecentTracks(libraryID, 7, 0)
	if err != nil {
		return MixResult{}, err
	}
	seedTracks := recentTracks
	if len(seedTracks) > settings.RecentTracks {
		seedTracks = seedTracks[:settings.RecentTracks]
	}
	for _, t := range seedTracks {
		if !seen[t.RatingKey] {
			mixTracks = append(mixTracks, t.RatingKey)
			seen[t.RatingKey] = true
		}
	}

	relatedPerSeed := settings.RelatedTracks
	if len(seedTracks) > 0 {
		relatedPerSeed = ceilDiv(settings.RelatedTracks, len(seedTracks))
	}
	target := settings.RecentTracks + settings.RelatedTracks
	for i := 0; i < len(seedTracks) && len(mixTracks) < target; i += plexLookupBatchSize {
		end := i + plexLookupBatchSize
		if end > len(seedTracks) {
			end = len(seedTracks)
		}
		batch := seedTracks[i:end]
		related := mapBatched(batch, plexLookupBatchSize, func(seed plex.Track) []plex.Track {
			tracks, _ := p.GetSimilarTracks(seed.RatingKey, relatedPerSeed)
			return tracks
		})
		for _, rs := range related {
			for _, t := range rs {
				if !seen[t.RatingKey] {
					mixTracks = append(mixTracks, t.RatingKey)
					seen[t.RatingKey] = true
				}
			}
		}
	}

	staleTracks, err := p.GetStalePlayedTracks(libraryID, settings.RediscoveryDays, settings.RediscoveryTracks, false)
	if err != nil {
		return MixResult{}, err
	}
	for _, t := range staleTracks {
		if !seen[t.RatingKey] {
			mixTracks = append(mixTracks, t.RatingKey)
			seen[t.RatingKey] = true
		}
	}

	return result(shuffle(mixTracks)), nil
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return a
	}
	return (a + b - 1) / b
}

// ---- Time Capsule ----

type TimeCapsuleSettings struct {
	TrackCount   int
	DaysAgo      int
	MaxPerArtist int
}

func (s *Service) GenerateTimeCapsule(serverURL, plexToken, libraryID string, settings TimeCapsuleSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)

	poolSize := settings.TrackCount * 10
	allTracks, err := p.GetStalePlayedTracks(libraryID, settings.DaysAgo, poolSize, true)
	if err != nil {
		return MixResult{}, err
	}
	if len(allTracks) == 0 {
		return result(nil), nil
	}

	type queue struct {
		tracks   []plex.Track
		selected int
	}
	byArtist := map[string]*queue{}
	var artistOrder []string
	for _, t := range allTracks {
		artist := t.GrandparentTitle
		if artist == "" {
			artist = "Unknown"
		}
		q, ok := byArtist[artist]
		if !ok {
			q = &queue{}
			byArtist[artist] = q
			artistOrder = append(artistOrder, artist)
		}
		q.tracks = append(q.tracks, t)
	}
	artistOrder = shuffle(artistOrder)

	var selected []plex.Track
	hasMore := true
	for len(selected) < settings.TrackCount && hasMore {
		hasMore = false
		for _, artist := range artistOrder {
			q := byArtist[artist]
			if len(selected) >= settings.TrackCount {
				break
			}
			if q.selected >= settings.MaxPerArtist || len(q.tracks) == 0 {
				continue
			}
			idx := rand.Intn(len(q.tracks))
			track := q.tracks[idx]
			q.tracks = append(q.tracks[:idx], q.tracks[idx+1:]...)
			selected = append(selected, track)
			q.selected++
			hasMore = true
		}
	}

	keys := make([]string, len(selected))
	for i, t := range selected {
		keys[i] = t.RatingKey
	}
	return result(shuffle(keys)), nil
}

// ---- New Music Mix ----

type NewMusicSettings struct {
	AlbumCount     int
	TracksPerAlbum int
}

func (s *Service) GenerateNewMusicMix(serverURL, plexToken, libraryID string, settings NewMusicSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)

	albums, err := p.GetRecentlyAddedAlbums(libraryID, settings.AlbumCount)
	if err != nil {
		return MixResult{}, err
	}

	var keys []string
	seen := map[string]bool{}
	for _, album := range albums {
		tracks, err := p.GetAlbumTracks(album.RatingKey)
		if err != nil {
			continue
		}
		shuffled := shuffle(tracks)
		if len(shuffled) > settings.TracksPerAlbum {
			shuffled = shuffled[:settings.TracksPerAlbum]
		}
		for _, t := range shuffled {
			if !seen[t.RatingKey] {
				keys = append(keys, t.RatingKey)
				seen[t.RatingKey] = true
			}
		}
	}

	return result(shuffle(keys)), nil
}

// ---- Custom Mix (advanced filters) ----

// YearRange is an inclusive [Min, Max] disjoint year band; nil bound = open
// on that side, matching config.yearRanges' {min?, max?} shape.
type YearRange struct{ Min, Max *int }

// CustomMixSettings mirrors generateCustomMix's settings object 1:1.
type CustomMixSettings struct {
	TrackCount int

	PlayedInLastDays    int
	NotPlayedInLastDays int
	AddedInLastDays     int

	ReleasedAfterYear  int
	ReleasedBeforeYear int
	YearRanges         []YearRange

	MinRating              int
	MaxRating              int
	MinPlayCount           *int
	MaxPlayCount           *int
	PopularTracksOnly      bool
	PopularTracksPerArtist int
	PopularArtistsOnly     bool
	MaxPopularArtists      int

	MinDuration    int
	MaxDuration    int
	MinTrackNumber int
	MaxTrackNumber int
	DiscNumber     int

	MinBitrate    int
	AudioCodec    []string
	MinSampleRate int
	LosslessOnly  bool

	Genres        []string
	ExcludeGenres []string
	Moods         []string
	ExcludeMoods  []string
	Styles        []string
	ExcludeStyles []string
	Collections   []string
	Labels        []string

	ArtistNames []string
	AlbumTitles []string

	SonicSeedTrackKey          string
	SonicSeedArtistKey         string
	SonicMaxDistance           float64
	SonicIncludeSameArtist     bool
	SonicIncludeSimilarArtists bool
	SonicUsePopularTracks      bool

	SortBy        string
	SortDirection string
}

func lowerSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[strings.ToLower(i)] = true
	}
	return m
}

func hasAny(tags []plex.Tag, want map[string]bool) bool {
	for _, t := range tags {
		if want[strings.ToLower(t.Tag)] {
			return true
		}
	}
	return false
}

// GenerateCustomMix ports generateCustomMix (mixes.ts:311-927). progress may
// be nil.
func (s *Service) GenerateCustomMix(serverURL, plexToken, libraryID string, settings CustomMixSettings, progress ProgressFunc) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	var tracks []plex.Track

	hasYearFilter := settings.ReleasedAfterYear != 0 || settings.ReleasedBeforeYear != 0 || len(settings.YearRanges) > 0
	hasArtistFilter := len(settings.ArtistNames) > 0
	usePopularOptimization := settings.PopularTracksOnly || (hasYearFilter && !settings.PopularTracksOnly)

	switch {
	case hasArtistFilter && usePopularOptimization:
		emit(progress, "fetching_artists", fmt.Sprintf("Fetching tracks from %d specified artists...", len(settings.ArtistNames)), 10)
		perArtistLimit := settings.PopularTracksPerArtist
		if perArtistLimit == 0 {
			perArtistLimit = maxInt(3, ceilDiv(settings.TrackCount, len(settings.ArtistNames)))
		}
		processed := 0
		for i := 0; i < len(settings.ArtistNames); i += plexLookupBatchSize {
			end := i + plexLookupBatchSize
			if end > len(settings.ArtistNames) {
				end = len(settings.ArtistNames)
			}
			batch := settings.ArtistNames[i:end]
			batchTracks := mapBatched(batch, plexLookupBatchSize, func(name string) []plex.Track {
				artist, err := p.SearchArtist(libraryID, name)
				if err != nil || artist == nil {
					return nil
				}
				ts, err := p.GetArtistPopularTracks(libraryID, artist.RatingKey, perArtistLimit)
				if err != nil {
					return nil
				}
				return ts
			})
			for _, ts := range batchTracks {
				tracks = append(tracks, ts...)
			}
			processed += len(batch)
			emit(progress, "fetching_artists", fmt.Sprintf("Processed %d/%d artists...", processed, len(settings.ArtistNames)), 10+int(float64(processed)/float64(len(settings.ArtistNames))*70))
		}

	case usePopularOptimization:
		emit(progress, "fetching_artists", "Fetching popular artists from Last.fm...", 10)
		maxArtists := settings.MaxPopularArtists
		if maxArtists == 0 {
			maxArtists = 20
		}
		lastfmArtists := lastfm.GetTopArtists(context.Background(), maxArtists*2)
		if len(lastfmArtists) == 0 {
			return MixResult{}, errors.New("failed to fetch popular artists from Last.fm. Please try again")
		}

		emit(progress, "matching_artists", fmt.Sprintf("Matching %d popular artists in your library...", len(lastfmArtists)), 20)
		matchedKeys := map[string]bool{}
		var matchedOrder []string
		matched := 0
		for i := 0; i < len(lastfmArtists) && len(matchedKeys) < maxArtists; i += plexLookupBatchSize {
			end := i + plexLookupBatchSize
			if end > len(lastfmArtists) {
				end = len(lastfmArtists)
			}
			batch := lastfmArtists[i:end]
			batchArtists := mapBatched(batch, plexLookupBatchSize, func(a lastfm.Artist) *plex.Track {
				artist, err := p.SearchArtist(libraryID, a.Name)
				if err != nil {
					return nil
				}
				return artist
			})
			for _, artist := range batchArtists {
				if artist != nil && !matchedKeys[artist.RatingKey] {
					matchedKeys[artist.RatingKey] = true
					matchedOrder = append(matchedOrder, artist.RatingKey)
					matched++
				}
			}
			emit(progress, "matching_artists", fmt.Sprintf("Matched %d/%d artists...", matched, len(lastfmArtists)), 20+int(float64(matched)/float64(len(lastfmArtists))*20))
		}
		if len(matchedOrder) == 0 {
			return MixResult{}, errors.New("none of the popular artists from Last.fm were found in your library. Try expanding your library or adjusting filters")
		}

		emit(progress, "fetching_popular", fmt.Sprintf("Fetching popular tracks from %d artists...", len(matchedOrder)), 40)
		perArtistLimit := settings.PopularTracksPerArtist
		if perArtistLimit == 0 {
			perArtistLimit = maxInt(3, ceilDiv(settings.TrackCount, len(matchedOrder)))
		}
		processed := 0
		for i := 0; i < len(matchedOrder); i += plexLookupBatchSize {
			end := i + plexLookupBatchSize
			if end > len(matchedOrder) {
				end = len(matchedOrder)
			}
			batch := matchedOrder[i:end]
			batchTracks := mapBatched(batch, plexLookupBatchSize, func(artistKey string) []plex.Track {
				ts, err := p.GetArtistPopularTracks(libraryID, artistKey, perArtistLimit)
				if err != nil {
					return nil
				}
				return ts
			})
			for _, ts := range batchTracks {
				tracks = append(tracks, ts...)
			}
			processed += len(batch)
			emit(progress, "fetching_popular", fmt.Sprintf("Processed %d/%d artists...", processed, len(matchedOrder)), 40+int(float64(processed)/float64(len(matchedOrder))*40))
		}

		emit(progress, "applying_filters", "Applying additional filters...", 80)
		tracks = applyClientSideFilters(tracks, settings)

	case settings.SonicSeedTrackKey != "" || settings.SonicSeedArtistKey != "":
		emit(progress, "sonic_analysis", "Analyzing sonic similarity...", 20)
		if settings.SonicUsePopularTracks && settings.SonicSeedArtistKey != "" {
			ts, err := p.GetArtistPopularTracks(libraryID, settings.SonicSeedArtistKey, settings.TrackCount)
			if err != nil {
				return MixResult{}, err
			}
			tracks = ts
		} else {
			seedKey := settings.SonicSeedTrackKey
			if seedKey == "" {
				seedKey = settings.SonicSeedArtistKey
			}
			maxDist := settings.SonicMaxDistance
			if maxDist == 0 {
				maxDist = 0.25
			}
			ts, err := p.GetSonicallySimilarTracks(seedKey, libraryID, plex.SonicSimilarOptions{
				Limit: settings.TrackCount * 3, MaxDistance: maxDist,
			})
			if err != nil {
				return MixResult{}, err
			}
			tracks = ts
		}

		emit(progress, "expanding_results", "Expanding results with similar artists...", 50)
		if settings.SonicSeedTrackKey != "" && settings.SonicIncludeSameArtist {
			seedTrack, _ := p.GetTrackDetails(settings.SonicSeedTrackKey)
			if seedTrack != nil && seedTrack.GrandparentRatingKey != "" {
				artistTracks, _ := p.GetArtistPopularTracks(libraryID, seedTrack.GrandparentRatingKey, 20)
				tracks = append(tracks, artistTracks...)
			}
		}
		if settings.SonicIncludeSimilarArtists {
			seedArtistKey := settings.SonicSeedArtistKey
			if seedArtistKey == "" && settings.SonicSeedTrackKey != "" {
				if seedTrack, _ := p.GetTrackDetails(settings.SonicSeedTrackKey); seedTrack != nil {
					seedArtistKey = seedTrack.GrandparentRatingKey
				}
			}
			if seedArtistKey != "" {
				similar, _ := p.GetSimilarTracks(seedArtistKey, 50)
				tracks = append(tracks, similar...)
			}
		}
		tracks = dedupeTracks(tracks)

	default:
		emit(progress, "fetching_tracks", "Fetching tracks from library...", 30)
		ts, err := p.GetTracksWithAdvancedFilters(libraryID, toAdvancedFilterOptions(settings))
		if err != nil {
			return MixResult{}, err
		}
		tracks = ts
	}

	if len(settings.YearRanges) > 0 {
		tracks = filterTracks(tracks, func(t plex.Track) bool {
			year := t.Year
			if year == 0 {
				year = t.ParentYear
			}
			if year == 0 {
				return false
			}
			for _, r := range settings.YearRanges {
				if (r.Min == nil || year >= *r.Min) && (r.Max == nil || year <= *r.Max) {
					return true
				}
			}
			return false
		})
	}

	emit(progress, "filtering", "Applying filters...", 70)

	needsPopularArtistFilter := settings.PopularArtistsOnly && !settings.PopularTracksOnly
	if len(settings.Genres) > 0 || len(settings.ExcludeGenres) > 0 || settings.MinRating > 0 || settings.MinPlayCount != nil || needsPopularArtistFilter {
		if needsPopularArtistFilter {
			emit(progress, "filtering_artists", "Filtering by popular artists (Last.fm data)...", 75)
			artistKeys := map[string]bool{}
			for _, t := range tracks {
				if t.GrandparentRatingKey != "" {
					artistKeys[t.GrandparentRatingKey] = true
				}
			}
			popularArtists := map[string]bool{}
			checked := 0
			for artistKey := range artistKeys {
				details, err := p.GetArtistDetails(artistKey)
				if err == nil && details != nil && details.RatingCount > 0 {
					popularArtists[artistKey] = true
				}
				checked++
				if checked%10 == 0 {
					emit(progress, "filtering_artists", fmt.Sprintf("Checked %d/%d artists for Last.fm data...", checked, len(artistKeys)), 75+int(float64(checked)/float64(len(artistKeys))*10))
				}
			}
			tracks = filterTracks(tracks, func(t plex.Track) bool {
				return t.GrandparentRatingKey != "" && popularArtists[t.GrandparentRatingKey]
			})
			if len(tracks) == 0 {
				slog.Warn("[Popular Artists Filter] no tracks found from popular artists")
			}
		}

		genresWant := lowerSet(settings.Genres)
		genresExclude := lowerSet(settings.ExcludeGenres)
		tracks = filterTracks(tracks, func(t plex.Track) bool {
			if len(settings.Genres) > 0 && !hasAny(t.Genre, genresWant) {
				return false
			}
			if len(settings.ExcludeGenres) > 0 && hasAny(t.Genre, genresExclude) {
				return false
			}
			if settings.MinRating > 0 && int(t.UserRating) < settings.MinRating {
				return false
			}
			if settings.MinPlayCount != nil && t.ViewCount < *settings.MinPlayCount {
				return false
			}
			return true
		})
	}

	emit(progress, "finalizing", "Finalizing track selection...", 85)

	if settings.SortBy == "random" {
		tracks = shuffle(tracks)
	}
	if len(tracks) > settings.TrackCount {
		tracks = tracks[:settings.TrackCount]
	}

	keys := make([]string, len(tracks))
	for i, t := range tracks {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func filterTracks(tracks []plex.Track, keep func(plex.Track) bool) []plex.Track {
	out := tracks[:0]
	for _, t := range tracks {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out
}

func dedupeTracks(tracks []plex.Track) []plex.Track {
	seen := map[string]bool{}
	out := make([]plex.Track, 0, len(tracks))
	for _, t := range tracks {
		if !seen[t.RatingKey] {
			seen[t.RatingKey] = true
			out = append(out, t)
		}
	}
	return out
}

// applyClientSideFilters ports the popular-tracks-optimization path's
// client-side filter block (mixes.ts:573-696) - filters not already handled
// by the Plex-side query used elsewhere.
func applyClientSideFilters(tracks []plex.Track, settings CustomMixSettings) []plex.Track {
	genresWant := lowerSet(settings.Genres)
	genresExclude := lowerSet(settings.ExcludeGenres)
	moodsWant := lowerSet(settings.Moods)
	moodsExclude := lowerSet(settings.ExcludeMoods)
	stylesWant := lowerSet(settings.Styles)
	stylesExclude := lowerSet(settings.ExcludeStyles)
	collectionsWant := lowerSet(settings.Collections)
	labelsWant := lowerSet(settings.Labels)
	codecsWant := lowerSet(settings.AudioCodec)

	return filterTracks(tracks, func(t plex.Track) bool {
		year := t.Year
		if year == 0 {
			year = t.ParentYear
		}
		if settings.ReleasedAfterYear != 0 && (year == 0 || year < settings.ReleasedAfterYear) {
			return false
		}
		if settings.ReleasedBeforeYear != 0 && (year == 0 || year > settings.ReleasedBeforeYear) {
			return false
		}
		if settings.MinDuration != 0 && t.Duration < int64(settings.MinDuration)*1000 {
			return false
		}
		if settings.MaxDuration != 0 && t.Duration > int64(settings.MaxDuration)*1000 {
			return false
		}
		if settings.MinTrackNumber != 0 && t.Index < settings.MinTrackNumber {
			return false
		}
		if settings.MaxTrackNumber != 0 && t.Index > settings.MaxTrackNumber {
			return false
		}
		if settings.DiscNumber != 0 && t.ParentIndex != settings.DiscNumber {
			return false
		}
		if settings.MinRating != 0 && int(t.UserRating) < settings.MinRating {
			return false
		}
		if settings.MaxRating != 0 && int(t.UserRating) > settings.MaxRating {
			return false
		}
		if settings.MinPlayCount != nil && t.ViewCount < *settings.MinPlayCount {
			return false
		}
		if settings.MaxPlayCount != nil && t.ViewCount > *settings.MaxPlayCount {
			return false
		}
		if len(settings.Genres) > 0 && !hasAny(t.Genre, genresWant) {
			return false
		}
		if len(settings.ExcludeGenres) > 0 && hasAny(t.Genre, genresExclude) {
			return false
		}
		if len(settings.Moods) > 0 && !hasAny(t.Mood, moodsWant) {
			return false
		}
		if len(settings.ExcludeMoods) > 0 && hasAny(t.Mood, moodsExclude) {
			return false
		}
		if len(settings.Styles) > 0 && !hasAny(t.Style, stylesWant) {
			return false
		}
		if len(settings.ExcludeStyles) > 0 && hasAny(t.Style, stylesExclude) {
			return false
		}
		if len(settings.Collections) > 0 && !hasAny(t.Collection, collectionsWant) {
			return false
		}
		if len(settings.Labels) > 0 && !labelsWant[strings.ToLower(t.ParentStudio)] {
			return false
		}
		if len(t.Media) > 0 {
			m := t.Media[0]
			if settings.MinBitrate != 0 && m.Bitrate < settings.MinBitrate {
				return false
			}
			if len(settings.AudioCodec) > 0 && !codecsWant[strings.ToLower(m.AudioCodec)] {
				return false
			}
			if settings.MinSampleRate != 0 && m.AudioSampleRate < settings.MinSampleRate {
				return false
			}
			if settings.LosslessOnly {
				codec := strings.ToLower(m.AudioCodec)
				lossless := codec == "flac" || codec == "alac" || codec == "ape" || codec == "wav"
				if !lossless {
					return false
				}
			}
		} else if settings.MinBitrate != 0 || len(settings.AudioCodec) > 0 || settings.MinSampleRate != 0 || settings.LosslessOnly {
			return false
		}
		return true
	})
}

func toAdvancedFilterOptions(settings CustomMixSettings) plex.AdvancedFilterOptions {
	return plex.AdvancedFilterOptions{
		PlayedInLastDays: settings.PlayedInLastDays, NotPlayedInLastDays: settings.NotPlayedInLastDays,
		AddedInLastDays:   settings.AddedInLastDays,
		ReleasedAfterYear: settings.ReleasedAfterYear, ReleasedBeforeYear: settings.ReleasedBeforeYear,
		MinRating: settings.MinRating, MaxRating: settings.MaxRating,
		MinPlayCount: settings.MinPlayCount, MaxPlayCount: settings.MaxPlayCount,
		MinDuration: settings.MinDuration, MaxDuration: settings.MaxDuration,
		MinTrackNumber: settings.MinTrackNumber, MaxTrackNumber: settings.MaxTrackNumber, DiscNumber: settings.DiscNumber,
		MinBitrate: settings.MinBitrate, AudioCodec: settings.AudioCodec, MinSampleRate: settings.MinSampleRate, LosslessOnly: settings.LosslessOnly,
		Genres: settings.Genres, ExcludeGenres: settings.ExcludeGenres,
		Moods: settings.Moods, ExcludeMoods: settings.ExcludeMoods,
		Styles: settings.Styles, ExcludeStyles: settings.ExcludeStyles,
		Collections: settings.Collections, Labels: settings.Labels,
		ArtistNames: settings.ArtistNames, AlbumTitles: settings.AlbumTitles,
		SortBy: settings.SortBy, SortDirection: settings.SortDirection,
		Limit: settings.TrackCount * 2,
	}
}

// ---- Sonic Mix ----

type SonicMixSettings struct {
	SeedTrackKey      string
	TrackCount        int
	MaxDistance       float64
	TempoRange        *plex.SonicRange
	EnergyRange       *plex.SonicRange
	DanceabilityRange *plex.SonicRange
}

func (s *Service) GenerateSonicMix(serverURL, plexToken, libraryID string, settings SonicMixSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	maxDist := settings.MaxDistance
	if maxDist == 0 {
		maxDist = 0.25
	}
	tracks, err := p.GetSonicallySimilarTracks(settings.SeedTrackKey, libraryID, plex.SonicSimilarOptions{
		MaxDistance: maxDist, Limit: settings.TrackCount,
		TempoRange: settings.TempoRange, EnergyRange: settings.EnergyRange, DanceabilityRange: settings.DanceabilityRange,
	})
	if err != nil {
		return MixResult{}, err
	}
	keys := make([]string, len(tracks))
	for i, t := range tracks {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}

// ---- All Mixes ----

type AllMixSettings struct {
	Weekly      WeeklyMixSettings
	Daily       DailyMixSettings
	TimeCapsule TimeCapsuleSettings
	NewMusic    NewMusicSettings
}

type AllMixesResult struct {
	Weekly, Daily, TimeCapsule, NewMusic MixResult
}

func (s *Service) GenerateAllMixes(serverURL, plexToken, libraryID string, settings AllMixSettings) (AllMixesResult, error) {
	weekly, err := s.GenerateWeeklyMix(serverURL, plexToken, libraryID, settings.Weekly)
	if err != nil {
		return AllMixesResult{}, err
	}
	daily, err := s.GenerateDailyMix(serverURL, plexToken, libraryID, settings.Daily)
	if err != nil {
		return AllMixesResult{}, err
	}
	timeCapsule, err := s.GenerateTimeCapsule(serverURL, plexToken, libraryID, settings.TimeCapsule)
	if err != nil {
		return AllMixesResult{}, err
	}
	newMusic, err := s.GenerateNewMusicMix(serverURL, plexToken, libraryID, settings.NewMusic)
	if err != nil {
		return AllMixesResult{}, err
	}
	return AllMixesResult{weekly, daily, timeCapsule, newMusic}, nil
}

// ---- Deep Cuts ----

type DeepCutsSettings struct {
	TrackCount   int
	MaxPlayCount int
	MinRating    int
}

func (s *Service) GenerateDeepCutsMix(serverURL, plexToken, libraryID string, settings DeepCutsSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	allTracks, err := p.GetTracksWithAdvancedFilters(libraryID, plex.AdvancedFilterOptions{
		SortBy: "playCount", SortDirection: "asc", Limit: settings.TrackCount * 3,
	})
	if err != nil {
		return MixResult{}, err
	}
	deepCuts := filterTracks(allTracks, func(t plex.Track) bool {
		if settings.MaxPlayCount != 0 && t.ViewCount > settings.MaxPlayCount {
			return false
		}
		if settings.MinRating != 0 && int(t.UserRating) < settings.MinRating {
			return false
		}
		return true
	})
	deepCuts = shuffle(deepCuts)
	if len(deepCuts) > settings.TrackCount {
		deepCuts = deepCuts[:settings.TrackCount]
	}
	keys := make([]string, len(deepCuts))
	for i, t := range deepCuts {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}

// ---- Artist Discovery ----

type ArtistDiscoverySettings struct {
	SeedArtistKeys    []string
	TracksPerArtist   int
	MaxSimilarArtists int
}

func (s *Service) GenerateArtistDiscoveryMix(serverURL, plexToken, libraryID string, settings ArtistDiscoverySettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)

	perSeed := mapBatched(settings.SeedArtistKeys, plexLookupBatchSize, func(artistKey string) []plex.Track {
		hubs, err := p.GetRelatedHubs(artistKey)
		if err != nil {
			slog.Error("[Mixes] failed to get similar artists", "artistKey", artistKey, "error", err)
			return nil
		}
		var similarHub *plex.Hub
		for i := range hubs {
			title := strings.ToLower(hubs[i].Title)
			if strings.Contains(title, "similar") || strings.Contains(title, "fans also like") {
				similarHub = &hubs[i]
				break
			}
		}
		if similarHub == nil || len(similarHub.Metadata) == 0 {
			return nil
		}
		similarArtists, err := plex.DecodeTracks(similarHub.Metadata)
		if err != nil {
			return nil
		}
		if len(similarArtists) > settings.MaxSimilarArtists {
			similarArtists = similarArtists[:settings.MaxSimilarArtists]
		}
		perArtist := mapBatched(similarArtists, plexLookupBatchSize, func(artist plex.Track) []plex.Track {
			ts, err := p.GetArtistPopularTracks(libraryID, artist.RatingKey, settings.TracksPerArtist)
			if err != nil {
				return nil
			}
			return ts
		})
		var out []plex.Track
		for _, ts := range perArtist {
			out = append(out, ts...)
		}
		return out
	})

	var allTracks []plex.Track
	for _, ts := range perSeed {
		allTracks = append(allTracks, ts...)
	}
	allTracks = dedupeTracks(allTracks)
	allTracks = shuffle(allTracks)

	keys := make([]string, len(allTracks))
	for i, t := range allTracks {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}

// ---- Mood Mix ----

type MoodMixSettings struct {
	Moods            []string
	TrackCount       int
	UseSonicAnalysis bool
}

func (s *Service) GenerateMoodMix(serverURL, plexToken, libraryID string, settings MoodMixSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	tracks, err := p.GetTracksWithAdvancedFilters(libraryID, plex.AdvancedFilterOptions{
		Moods: settings.Moods, Limit: settings.TrackCount * 2,
	})
	if err != nil {
		return MixResult{}, err
	}

	if settings.UseSonicAnalysis && len(tracks) > 0 {
		seed := tracks[0]
		sonicTracks, err := p.GetSonicallySimilarTracks(seed.RatingKey, libraryID, plex.SonicSimilarOptions{Limit: settings.TrackCount})
		if err == nil {
			moodsWant := lowerSet(settings.Moods)
			moodFiltered := filterTracks(sonicTracks, func(t plex.Track) bool { return hasAny(t.Mood, moodsWant) })
			if len(moodFiltered) >= settings.TrackCount/2 {
				if len(moodFiltered) > settings.TrackCount {
					moodFiltered = moodFiltered[:settings.TrackCount]
				}
				keys := make([]string, len(moodFiltered))
				for i, t := range moodFiltered {
					keys[i] = t.RatingKey
				}
				return result(keys), nil
			}
		}
	}

	tracks = shuffle(tracks)
	if len(tracks) > settings.TrackCount {
		tracks = tracks[:settings.TrackCount]
	}
	keys := make([]string, len(tracks))
	for i, t := range tracks {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}

// ---- Era Mix ----

type EraMixSettings struct {
	StartYear, EndYear int
	TrackCount         int
	UsePopularTracks   bool
}

func (s *Service) GenerateEraMix(serverURL, plexToken, libraryID string, settings EraMixSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	sortBy := "random"
	if settings.UsePopularTracks {
		sortBy = "playCount"
	}
	tracks, err := p.GetTracksWithAdvancedFilters(libraryID, plex.AdvancedFilterOptions{
		ReleasedAfterYear: settings.StartYear, ReleasedBeforeYear: settings.EndYear,
		SortBy: sortBy, SortDirection: "desc", Limit: settings.TrackCount * 2,
	})
	if err != nil {
		return MixResult{}, err
	}
	if !settings.UsePopularTracks {
		tracks = shuffle(tracks)
	}
	if len(tracks) > settings.TrackCount {
		tracks = tracks[:settings.TrackCount]
	}
	keys := make([]string, len(tracks))
	for i, t := range tracks {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}

// ---- Genre Evolution ----

type GenreEvolutionSettings struct {
	Genre           string
	TrackCount      int
	TracksPerDecade int
}

func (s *Service) GenerateGenreEvolutionMix(serverURL, plexToken, libraryID string, settings GenreEvolutionSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	allTracks, err := p.GetTracksWithAdvancedFilters(libraryID, plex.AdvancedFilterOptions{
		Genres: []string{settings.Genre}, SortBy: "releaseDate", SortDirection: "asc", Limit: settings.TrackCount * 3,
	})
	if err != nil {
		return MixResult{}, err
	}
	if len(allTracks) == 0 {
		return result(nil), nil
	}

	byDecade := map[int][]plex.Track{}
	var decades []int
	for _, t := range allTracks {
		if t.Year == 0 {
			continue
		}
		decade := (t.Year / 10) * 10
		if _, ok := byDecade[decade]; !ok {
			decades = append(decades, decade)
		}
		byDecade[decade] = append(byDecade[decade], t)
	}
	sort.Ints(decades)

	tracksPerDecade := settings.TracksPerDecade
	if tracksPerDecade == 0 && len(decades) > 0 {
		tracksPerDecade = ceilDiv(settings.TrackCount, len(decades))
	}

	var selected []plex.Track
	for _, decade := range decades {
		decadeTracks := shuffle(byDecade[decade])
		if len(decadeTracks) > tracksPerDecade {
			decadeTracks = decadeTracks[:tracksPerDecade]
		}
		selected = append(selected, decadeTracks...)
	}
	if len(selected) > settings.TrackCount {
		selected = selected[:settings.TrackCount]
	}
	keys := make([]string, len(selected))
	for i, t := range selected {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}

// ---- Artist Journey ----

type ArtistJourneySettings struct {
	ArtistKey        string
	TracksPerAlbum   int
	UsePopularTracks bool
}

func (s *Service) GenerateArtistJourneyMix(serverURL, plexToken, _libraryID string, settings ArtistJourneySettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	albums, err := p.GetArtistAlbums(settings.ArtistKey)
	if err != nil {
		return MixResult{}, err
	}
	sort.SliceStable(albums, func(i, j int) bool { return albums[i].Year < albums[j].Year })

	var allTracks []plex.Track
	for _, album := range albums {
		albumTracks, err := p.GetAlbumTracks(album.RatingKey)
		if err != nil {
			slog.Error("[Mixes] failed to get tracks for album", "album", album.RatingKey, "error", err)
			continue
		}
		if settings.UsePopularTracks {
			sort.SliceStable(albumTracks, func(i, j int) bool { return albumTracks[i].ViewCount > albumTracks[j].ViewCount })
		}
		if len(albumTracks) > settings.TracksPerAlbum {
			albumTracks = albumTracks[:settings.TracksPerAlbum]
		}
		allTracks = append(allTracks, albumTracks...)
	}

	keys := make([]string, len(allTracks))
	for i, t := range allTracks {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}

// ---- Workout Mix ----

type WorkoutMixSettings struct {
	TrackCount                               int
	WarmupTracks, PeakTracks, CooldownTracks int
	MinTempo, MaxTempo                       float64
}

func (s *Service) GenerateWorkoutMix(serverURL, plexToken, libraryID string, settings WorkoutMixSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	allTracks, err := p.GetTracksWithAdvancedFilters(libraryID, plex.AdvancedFilterOptions{Limit: settings.TrackCount * 3})
	if err != nil {
		return MixResult{}, err
	}

	withTempo := filterTracks(append([]plex.Track{}, allTracks...), func(t plex.Track) bool {
		return t.MusicAnalysis != nil && t.MusicAnalysis.Tempo != 0
	})
	sort.SliceStable(withTempo, func(i, j int) bool { return withTempo[i].MusicAnalysis.Tempo < withTempo[j].MusicAnalysis.Tempo })

	if len(withTempo) == 0 {
		fallback := shuffle(allTracks)
		if len(fallback) > settings.TrackCount {
			fallback = fallback[:settings.TrackCount]
		}
		keys := make([]string, len(fallback))
		for i, t := range fallback {
			keys[i] = t.RatingKey
		}
		return result(keys), nil
	}

	third := len(withTempo) / 3
	low := shuffle(withTempo[:third])
	med := shuffle(withTempo[third : third*2])
	high := shuffle(withTempo[third*2:])

	var workout []plex.Track
	seen := map[string]bool{}
	add := func(src []plex.Track) {
		for _, t := range src {
			if !seen[t.RatingKey] {
				workout = append(workout, t)
				seen[t.RatingKey] = true
			}
		}
	}

	add(sliceUpTo(low, 0, settings.WarmupTracks))
	buildTracks := (settings.TrackCount - settings.WarmupTracks - settings.PeakTracks - settings.CooldownTracks) / 2
	add(sliceUpTo(med, 0, buildTracks))
	add(sliceUpTo(high, 0, settings.PeakTracks))
	add(sliceUpTo(med, buildTracks, buildTracks*2))
	add(sliceUpTo(low, settings.WarmupTracks, settings.WarmupTracks+settings.CooldownTracks))

	keys := make([]string, len(workout))
	for i, t := range workout {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}

func sliceUpTo(tracks []plex.Track, from, to int) []plex.Track {
	if from < 0 {
		from = 0
	}
	if from > len(tracks) {
		from = len(tracks)
	}
	if to > len(tracks) {
		to = len(tracks)
	}
	if to < from {
		return nil
	}
	return tracks[from:to]
}

// ---- Forgotten Favorites ----

type ForgottenFavoritesSettings struct {
	TrackCount      int
	MinPlayCount    int
	NotPlayedInDays int
}

func (s *Service) GenerateForgottenFavoritesMix(serverURL, plexToken, libraryID string, settings ForgottenFavoritesSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	minPlay := settings.MinPlayCount
	tracks, err := p.GetTracksWithAdvancedFilters(libraryID, plex.AdvancedFilterOptions{
		MinPlayCount: &minPlay, NotPlayedInLastDays: settings.NotPlayedInDays,
		SortBy: "playCount", SortDirection: "desc", Limit: settings.TrackCount * 2,
	})
	if err != nil {
		return MixResult{}, err
	}
	tracks = shuffle(tracks)
	if len(tracks) > settings.TrackCount {
		tracks = tracks[:settings.TrackCount]
	}
	keys := make([]string, len(tracks))
	for i, t := range tracks {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}

// ---- Genre Blend ----

type GenreBlendSettings struct {
	Genres           []string
	TrackCount       int
	RequireAllGenres bool
}

func (s *Service) GenerateGenreBlendMix(serverURL, plexToken, libraryID string, settings GenreBlendSettings) (MixResult, error) {
	p := s.client(serverURL, plexToken)
	tracks, err := p.GetTracksWithAdvancedFilters(libraryID, plex.AdvancedFilterOptions{
		Genres: settings.Genres, Limit: settings.TrackCount * 3,
	})
	if err != nil {
		return MixResult{}, err
	}

	filtered := tracks
	if settings.RequireAllGenres {
		want := lowerSet(settings.Genres)
		filtered = filterTracks(tracks, func(t plex.Track) bool {
			have := map[string]bool{}
			for _, g := range t.Genre {
				have[strings.ToLower(g.Tag)] = true
			}
			for g := range want {
				if !have[g] {
					return false
				}
			}
			return true
		})
	}

	filtered = shuffle(filtered)
	if len(filtered) > settings.TrackCount {
		filtered = filtered[:settings.TrackCount]
	}
	keys := make([]string, len(filtered))
	for i, t := range filtered {
		keys[i] = t.RatingKey
	}
	return result(keys), nil
}
