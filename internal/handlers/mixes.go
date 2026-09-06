// Package handlers: mixes.go ports routes/mixes.ts to HTMX. Rather than a
// bespoke SSE progress-streaming endpoint per generation session (the TS
// mixGenerationSessions EventEmitter map), mix generation runs through the
// same actionqueue + notifications pattern every other background job in
// this app uses: POST /mixes/generate enqueues the job and returns
// immediately, and the caller watches it finish via the notification bell's
// existing SSE stream (handlers/notifications.go) - one live-progress
// mechanism for the whole app instead of two competing ones.
package handlers

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/mixes"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

type MixesHandler struct {
	DB            *sql.DB
	PlexAuth      *auth.PlexClient
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
	Mixes         *mixes.Service
}

func RegisterMixes(r chi.Router, mw *auth.Middleware, h *MixesHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/mixes", h.page)
		r.Get("/mixes/quick-settings/{mixType}", h.quickSettingsForm)
		r.Get("/mixes/advanced-form/{mixType}", h.advancedForm)
		r.Get("/mixes/custom-form", h.customForm)
		r.Post("/mixes/generate", h.generate)
	})
}

func (h *MixesHandler) page(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	userServer, _ := db.GetUserServer(h.DB, user.ID)

	data := map[string]any{"User": user, "HasServer": userServer != nil && userServer.LibraryID.Valid}
	if userServer != nil && userServer.LibraryID.Valid {
		client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(user.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")
		data["Genres"] = client.GetLibraryGenres(userServer.LibraryID.String)
		data["Moods"] = client.GetLibraryMoods(userServer.LibraryID.String)
	}
	h.Tmpl.RenderPage(w, "mixes", data)
}

// quickMixMeta holds the copy shown in the Quick Mix Settings modal (title,
// description, default playlist name), keyed by mixType. Mirrors the card
// copy in templates/mixes.html and the defaultName values in run() below.
var quickMixMeta = map[string][3]string{
	"weekly":              {"Weekly Mix", "Your most-played artists right now", "Your Weekly Mix"},
	"daily":               {"Daily Mix", "Recent plays, plus related tracks and rediscoveries", "Daily Mix"},
	"timecapsule":         {"Time Capsule", "Anything you haven't played in a while, regardless of how often you used to play it", "Time Capsule"},
	"forgotten-favorites": {"Forgotten Favorites", "Tracks you used to play a lot but have stopped playing recently", "Forgotten Favorites"},
	"deep-cuts":           {"Deep Cuts Mix", "Tracks with low play counts you've likely never given much attention", "Deep Cuts"},
	"workout":             {"Workout Mix", "A progressive tempo build for exercise", "Workout Mix"},
	"newmusic":            {"New Music Mix", "Recently added albums", "New Music Mix"},
}

// quickSettingsForm renders the Quick Mix Settings modal (ported from
// QuickMixSettingsModal.tsx) for one of the "listening history" / "library
// structure" quick mixes - the simple ones with only a couple of numeric
// sliders, as opposed to the advanced-form mixes below.
func (h *MixesHandler) quickSettingsForm(w http.ResponseWriter, r *http.Request) {
	mixType := chi.URLParam(r, "mixType")
	meta, ok := quickMixMeta[mixType]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/mix_quick_settings_form.html", map[string]any{
		"MixType": mixType, "Title": meta[0], "Description": meta[1], "DefaultName": meta[2],
	})
}

var advancedMixTitles = map[string]string{
	"artist-discovery": "Artist Discovery Mix",
	"mood":             "Mood Mix",
	"era":              "Era Mix",
	"genre-evolution":  "Genre Evolution Mix",
	"artist-journey":   "Artist Journey Mix",
	"genre-blend":      "Genre Blend Mix",
}

var advancedMixDefaultNames = map[string]string{
	"artist-discovery": "Artist Discovery",
	"mood":             "Mood Mix",
	"era":              "1970s Mix",
	"genre-evolution":  "",
	"artist-journey":   "Artist Journey",
	"genre-blend":      "Genre Blend",
}

// advancedForm renders the modal ported from components/AdvancedMixModal.tsx.
// Genres/moods are loaded the same way the mixes page itself does, for the
// mood/genre-evolution/genre-blend variants that offer tag pickers.
func (h *MixesHandler) advancedForm(w http.ResponseWriter, r *http.Request) {
	mixType := chi.URLParam(r, "mixType")
	title, ok := advancedMixTitles[mixType]
	if !ok {
		http.NotFound(w, r)
		return
	}
	user := auth.CurrentUser(r)
	userServer, _ := db.GetUserServer(h.DB, user.ID)
	data := map[string]any{"MixType": mixType, "Title": title, "DefaultName": advancedMixDefaultNames[mixType]}
	if userServer != nil && userServer.LibraryID.Valid {
		client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(user.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")
		data["Genres"] = client.GetLibraryGenres(userServer.LibraryID.String)
		data["Moods"] = client.GetLibraryMoods(userServer.LibraryID.String)
	}
	h.Tmpl.RenderPartial(w, "partials/mix_advanced_form.html", data)
}

// customForm renders the modal ported from pages/CustomMixModal.tsx, scoped
// to the fields mixes.CustomMixSettings (and the "custom" case in run()
// below) actually accepts - see the doc comment in the template itself for
// what was left out and why.
func (h *MixesHandler) customForm(w http.ResponseWriter, r *http.Request) {
	h.Tmpl.RenderPartial(w, "partials/mix_custom_form.html", nil)
}

// generate dispatches on the mixType form field, mirroring each POST
// /api/mixes/* route in mixes.ts as one switch case, then enqueues the
// actual generation + playlist creation as a background action-queue job so
// the request returns immediately (matching how every other long-running
// action in this app is handled, instead of the TS route's synchronous
// await-then-respond).
func (h *MixesHandler) generate(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	mixType := r.FormValue("mixType")
	if mixType == "" {
		http.Error(w, "mixType is required", http.StatusBadRequest)
		return
	}

	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "No music library selected. Please select a library first.", http.StatusBadRequest)
		return
	}

	form := r.PostForm
	title := formLabel(mixType)
	jobID, _ := h.Queue.Enqueue(user.ID, "Generating "+title, notifications.TypeMix, func(notificationID string) error {
		return h.run(user.ID, userServer, mixType, form, notificationID)
	})

	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{
		"Notifications": h.Notifications.List(user.ID),
		"JobID":         jobID,
	})
}

// resolveArtistKey looks up a Plex ratingKey by artist name - the server-side
// stand-in for the search-as-you-type dropdown AdvancedMixModal.tsx has
// against /api/search/artists, which this Go server doesn't implement.
func resolveArtistKey(serverURL, token, clientID, libraryID, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("artist name is required")
	}
	client := plex.NewClient(serverURL, token, clientID, "Playlist Lab")
	artist, err := client.SearchArtist(libraryID, name)
	if err != nil {
		return "", err
	}
	if artist == nil {
		return "", fmt.Errorf("no artist found matching %q", name)
	}
	return artist.RatingKey, nil
}

func formLabel(mixType string) string {
	return strings.Title(strings.ReplaceAll(mixType, "-", " ")) + " Mix"
}

// run does the actual Plex work for one generate request: builds the mix
// settings from form values (applying the same defaults as the TS routes),
// generates the track list, and creates the resulting Plex playlist.
// Progress is reported through the notification's own Detail field - this
// handler's caller (actionqueue) is what makes that visible over SSE.
func (h *MixesHandler) run(userID int64, userServer *db.UserServer, mixType string, form map[string][]string, notificationID string) error {
	get := func(key, def string) string {
		if v := form[key]; len(v) > 0 && v[0] != "" {
			return v[0]
		}
		return def
	}
	getInt := func(key string, def int) int {
		if v := form[key]; len(v) > 0 && v[0] != "" {
			if n, err := strconv.Atoi(v[0]); err == nil {
				return n
			}
		}
		return def
	}
	getCSV := func(key string) []string {
		v := get(key, "")
		if v == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}

	user, err := db.GetUserByID(h.DB, userID)
	if err != nil {
		return err
	}
	serverURL := userServer.ServerURL
	token := plex.ResolveToken(user.PlexToken, userServer.AccessToken.String)
	libraryID := userServer.LibraryID.String

	progress := func(stage, message string, pct int) {
		p := pct
		detail := message
		h.Notifications.Update(userID, notificationID, notifications.Patch{Detail: &detail, Progress: &p})
	}

	var (
		result      mixes.MixResult
		defaultName string
	)

	switch mixType {
	case "weekly":
		result, err = h.Mixes.GenerateWeeklyMix(serverURL, token, libraryID, mixes.WeeklyMixSettings{
			TopArtists: getInt("topArtists", 10), TracksPerArtist: getInt("tracksPerArtist", 5),
		})
		defaultName = "Your Weekly Mix"
	case "daily":
		result, err = h.Mixes.GenerateDailyMix(serverURL, token, libraryID, mixes.DailyMixSettings{
			RecentTracks: getInt("recentTracks", 20), RelatedTracks: getInt("relatedTracks", 15),
			RediscoveryTracks: getInt("rediscoveryTracks", 15), RediscoveryDays: getInt("rediscoveryDays", 90),
		})
		defaultName = "Daily Mix"
	case "timecapsule":
		result, err = h.Mixes.GenerateTimeCapsule(serverURL, token, libraryID, mixes.TimeCapsuleSettings{
			TrackCount: getInt("trackCount", 50), DaysAgo: getInt("daysAgo", 365), MaxPerArtist: getInt("maxPerArtist", 3),
		})
		defaultName = "Time Capsule"
	case "newmusic":
		result, err = h.Mixes.GenerateNewMusicMix(serverURL, token, libraryID, mixes.NewMusicSettings{
			AlbumCount: getInt("albumCount", 10), TracksPerAlbum: getInt("tracksPerAlbum", 3),
		})
		defaultName = "New Music Mix"
	case "custom":
		var minPlay, maxPlay *int
		if v := getInt("minPlayCount", -1); v >= 0 {
			minPlay = &v
		}
		if v := getInt("maxPlayCount", -1); v >= 0 {
			maxPlay = &v
		}
		result, err = h.Mixes.GenerateCustomMix(serverURL, token, libraryID, mixes.CustomMixSettings{
			TrackCount:          getInt("trackCount", 50),
			PlayedInLastDays:    getInt("playedInLastDays", 0),
			NotPlayedInLastDays: getInt("notPlayedInLastDays", 0),
			AddedInLastDays:     getInt("addedInLastDays", 0),
			ReleasedAfterYear:   getInt("releasedAfterYear", 0),
			ReleasedBeforeYear:  getInt("releasedBeforeYear", 0),
			MinRating:           getInt("minRating", 0),
			MaxRating:           getInt("maxRating", 0),
			MinPlayCount:        minPlay,
			MaxPlayCount:        maxPlay,
			Genres:              getCSV("genres"),
			ExcludeGenres:       getCSV("excludeGenres"),
			Moods:               getCSV("moods"),
			Styles:              getCSV("styles"),
			ArtistNames:         getCSV("artistNames"),
			PopularTracksOnly:   get("popularTracksOnly", "") == "true",
			PopularArtistsOnly:  get("popularArtistsOnly", "") == "true",
			SortBy:              get("sortBy", "random"),
			SortDirection:       get("sortDirection", "desc"),
		}, progress)
		defaultName = "Custom Mix"
	case "sonic":
		result, err = h.Mixes.GenerateSonicMix(serverURL, token, libraryID, mixes.SonicMixSettings{
			SeedTrackKey: get("seedTrackKey", ""), TrackCount: getInt("trackCount", 50),
		})
		defaultName = "Sonic Mix"
	case "deep-cuts":
		result, err = h.Mixes.GenerateDeepCutsMix(serverURL, token, libraryID, mixes.DeepCutsSettings{
			TrackCount: getInt("trackCount", 50), MaxPlayCount: getInt("maxPlayCount", 5), MinRating: getInt("minRating", 0),
		})
		defaultName = "Deep Cuts"
	case "artist-discovery":
		seed := get("seedArtistKey", "")
		if seed == "" {
			seed, err = resolveArtistKey(serverURL, token, h.PlexAuth.ClientID, libraryID, get("seedArtistName", ""))
			if err != nil {
				return err
			}
		}
		result, err = h.Mixes.GenerateArtistDiscoveryMix(serverURL, token, libraryID, mixes.ArtistDiscoverySettings{
			SeedArtistKeys: []string{seed}, TracksPerArtist: getInt("tracksPerArtist", 3), MaxSimilarArtists: getInt("trackCount", 50),
		})
		defaultName = "Artist Discovery"
	case "mood":
		result, err = h.Mixes.GenerateMoodMix(serverURL, token, libraryID, mixes.MoodMixSettings{
			Moods: getCSV("moods"), TrackCount: getInt("trackCount", 50), UseSonicAnalysis: get("useSonicAnalysis", "") == "true",
		})
		defaultName = strings.Join(getCSV("moods"), " & ") + " Mix"
	case "era":
		startYear, endYear := getInt("startYear", 0), getInt("endYear", 0)
		result, err = h.Mixes.GenerateEraMix(serverURL, token, libraryID, mixes.EraMixSettings{
			StartYear: startYear, EndYear: endYear, TrackCount: getInt("trackCount", 50),
		})
		defaultName = fmt.Sprintf("%ds Mix", startYear)
	case "genre-evolution":
		genre := get("genre", "")
		result, err = h.Mixes.GenerateGenreEvolutionMix(serverURL, token, libraryID, mixes.GenreEvolutionSettings{
			Genre: genre, TrackCount: getInt("trackCount", 50), TracksPerDecade: getInt("tracksPerDecade", 5),
		})
		defaultName = genre + " Evolution"
	case "artist-journey":
		artistKey := get("artistKey", "")
		if artistKey == "" {
			artistKey, err = resolveArtistKey(serverURL, token, h.PlexAuth.ClientID, libraryID, get("artistName", ""))
			if err != nil {
				return err
			}
		}
		result, err = h.Mixes.GenerateArtistJourneyMix(serverURL, token, libraryID, mixes.ArtistJourneySettings{
			ArtistKey: artistKey, TracksPerAlbum: getInt("tracksPerAlbum", 3),
		})
		defaultName = "Artist Journey"
	case "workout":
		result, err = h.Mixes.GenerateWorkoutMix(serverURL, token, libraryID, mixes.WorkoutMixSettings{
			TrackCount: getInt("trackCount", 50), WarmupTracks: getInt("warmupTracks", 5),
			PeakTracks: getInt("peakTracks", 30), CooldownTracks: getInt("cooldownTracks", 5),
		})
		defaultName = "Workout Mix"
	case "forgotten-favorites":
		result, err = h.Mixes.GenerateForgottenFavoritesMix(serverURL, token, libraryID, mixes.ForgottenFavoritesSettings{
			TrackCount: getInt("trackCount", 50), MinPlayCount: getInt("minPlayCount", 10), NotPlayedInDays: getInt("notPlayedDays", 180),
		})
		defaultName = "Forgotten Favorites"
	case "genre-blend":
		genres := getCSV("genres")
		result, err = h.Mixes.GenerateGenreBlendMix(serverURL, token, libraryID, mixes.GenreBlendSettings{
			Genres: genres, TrackCount: getInt("trackCount", 50), RequireAllGenres: getInt("minGenres", 2) > 1,
		})
		defaultName = strings.Join(genres, " + ") + " Blend"
	default:
		return fmt.Errorf("unsupported mix type: %s", mixType)
	}
	if err != nil {
		return err
	}
	if result.TrackCount == 0 {
		status := notifications.StatusError
		detail := "No tracks found matching this mix's criteria."
		h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return nil
	}

	playlistName := get("playlistName", defaultName)
	client := plex.NewClient(serverURL, token, h.PlexAuth.ClientID, "Playlist Lab")
	trackURIs := make([]string, len(result.TrackKeys))
	for i, key := range result.TrackKeys {
		trackURIs[i] = client.BuildTrackURI(key, userServer.ServerClientID)
	}
	libraryURI := client.BuildLibraryURI(libraryID, userServer.ServerClientID)

	playlist, err := client.CreatePlaylist(playlistName, libraryURI, trackURIs)
	if err != nil {
		return err
	}
	if _, err := db.CreatePlaylistRow(h.DB, userID, playlist.RatingKey, playlistName, "mix", ""); err != nil {
		slog.Error("failed to record mix playlist", "error", err)
	}

	status := notifications.StatusSuccess
	detail := fmt.Sprintf("Created \"%s\" with %d tracks", playlistName, result.TrackCount)
	pct := 100
	h.Notifications.Update(userID, notificationID, notifications.Patch{Status: &status, Detail: &detail, Progress: &pct})
	return nil
}
