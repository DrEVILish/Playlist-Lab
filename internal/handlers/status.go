// Package handlers: status.go ports StatusReportsModal.tsx to a full page
// (the header no longer opens it as a modal - see templates/layout.html's
// header nav and its doc comment) built entirely from data other handlers
// already load: Plex playlists (for the per-source/track/duration tallies),
// db.GetUserMissingTracks (missing count) and db.GetUserRecentExecutions
// (schedule run history), all summed server-side instead of shipped to the
// client as JSON for a useMemo to reduce.
package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

type StatusHandler struct {
	DB       *sql.DB
	PlexAuth *auth.PlexClient
	Tmpl     *Templates
}

func RegisterStatus(r chi.Router, mw *auth.Middleware, h *StatusHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/status", h.page)
	})
}

type sourceCount struct {
	Label string
	Count int
}

type freqCount struct {
	Freq  string
	Count int
}

// formatDurationHM mirrors StatusReportsModal.tsx's formatDuration: total
// playlist duration in ms as "Nd Nh" (days omitted once under 24h).
func formatDurationHM(ms int) string {
	totalHours := ms / 3600000
	days := totalHours / 24
	hours := totalHours % 24
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	return fmt.Sprintf("%dh", hours)
}

func (h *StatusHandler) page(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	data := map[string]any{"User": user}

	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		h.Tmpl.RenderPage(w, r, "status", data)
		return
	}

	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(user.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")
	plexPlaylists, err := client.GetPlaylists()
	if err != nil {
		data["PlexError"] = true
		h.Tmpl.RenderPage(w, r, "status", data)
		return
	}

	tracked, _ := db.GetUserPlaylists(h.DB, user.ID)
	sourceByPlexID := make(map[string]string, len(tracked))
	for _, p := range tracked {
		sourceByPlexID[p.PlexPlaylistID] = p.Source
	}

	var totalTracks, totalDuration, smartCount int
	bySource := map[string]int{}
	for _, p := range plexPlaylists {
		totalTracks += p.LeafCount
		totalDuration += int(p.Duration)
		if p.Smart {
			smartCount++
		}
		label := "Plex"
		if s, ok := sourceByPlexID[p.RatingKey]; ok && s != "plex" {
			label = strings.ToUpper(s[:1]) + s[1:]
		}
		bySource[label]++
	}
	sourceList := make([]sourceCount, 0, len(bySource))
	for label, count := range bySource {
		sourceList = append(sourceList, sourceCount{label, count})
	}
	sort.Slice(sourceList, func(i, j int) bool { return sourceList[i].Count > sourceList[j].Count })

	missingTracks, _ := db.GetUserMissingTracks(h.DB, user.ID)

	schedules, _ := db.GetUserSchedules(h.DB, user.ID)
	byFreq := map[string]int{}
	for _, s := range schedules {
		byFreq[s.Frequency]++
	}
	freqList := make([]freqCount, 0, len(byFreq))
	for freq, count := range byFreq {
		freqList = append(freqList, freqCount{freq, count})
	}
	sort.Slice(freqList, func(i, j int) bool { return freqList[i].Count > freqList[j].Count })

	executions, _ := db.GetUserRecentExecutions(h.DB, user.ID, 100)
	var succeeded, failed int
	for _, e := range executions {
		switch e.Status {
		case "success":
			succeeded++
		case "failed":
			failed++
		}
	}

	data["TotalPlaylists"] = len(plexPlaylists)
	data["TotalTracks"] = totalTracks
	data["TotalDuration"] = formatDurationHM(totalDuration)
	data["MissingCount"] = len(missingTracks)
	data["BySource"] = sourceList
	data["TotalSchedules"] = len(schedules)
	data["ByFrequency"] = freqList
	data["Succeeded"] = succeeded
	data["Failed"] = failed
	data["SmartCount"] = smartCount
	h.Tmpl.RenderPage(w, r, "status", data)
}
