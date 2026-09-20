package handlers

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

// sortedNotifications orders a user's feed to match
// NotificationCenter.tsx's display order: active jobs pinned above
// finished ones, most-recently-updated first within each group.
func sortedNotifications(list []notifications.Notification) []notifications.Notification {
	sort.SliceStable(list, func(i, j int) bool {
		iActive := list[i].Status == notifications.StatusInProgress
		jActive := list[j].Status == notifications.StatusInProgress
		if iActive != jActive {
			return iActive
		}
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	return list
}

// NotificationsHandler ports routes/notifications.ts to HTMX: instead of
// streaming JSON for client-side JS to render, the SSE endpoint pushes a
// pre-rendered HTML fragment (an "HTML over SSE" pattern) that
// htmx-ext-sse swaps directly into the bell dropdown - see the rewrite
// plan's HTMX/SSE section for the rationale.
type NotificationsHandler struct {
	Store *notifications.Store
	Tmpl  *Templates
}

func RegisterNotifications(r chi.Router, mw *auth.Middleware, h *NotificationsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/notifications/stream", h.stream)
		r.Post("/notifications/{id}/dismiss", h.dismiss)
		r.Post("/notifications/clear", h.clear)
	})
}

func (h *NotificationsHandler) render(w http.ResponseWriter, userID int64) {
	h.Tmpl.RenderPartial(w, "partials/notifications.html", notificationsViewData(h.Store.List(userID)))
}

// notificationsViewData bundles the sorted list with the two derived flags
// notifications.html needs for its "Clear complete"/"Clear all" buttons
// (NotificationCenter.tsx: shown only when there's something they'd
// actually affect), which html/template can't compute mid-range itself.
func notificationsViewData(list []notifications.Notification) map[string]any {
	hasSuccess, hasNonInProgress, hasInProgress := false, false, false
	for _, n := range list {
		if n.Status == notifications.StatusSuccess {
			hasSuccess = true
		}
		if n.Status != notifications.StatusInProgress {
			hasNonInProgress = true
		} else {
			hasInProgress = true
		}
	}
	return map[string]any{
		"Notifications":    sortedNotifications(list),
		"HasSuccess":       hasSuccess,
		"HasNonInProgress": hasNonInProgress,
		"Count":            len(list),
		"HasInProgress":    hasInProgress,
	}
}

func (h *NotificationsHandler) dismiss(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	h.Store.Dismiss(user.ID, chi.URLParam(r, "id"))
	h.render(w, user.ID)
}

func (h *NotificationsHandler) clear(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	h.Store.Clear(user.ID, r.FormValue("completedOnly") == "true")
	h.render(w, user.ID)
}

// stream is a Server-Sent Events endpoint: one event named "notification"
// per change to this user's feed, each carrying the whole re-rendered list
// (matching the Node route's "send the whole feed, not a diff" design -
// the payload is small and this avoids ever needing to reconcile partial
// updates client-side).
func (h *NotificationsHandler) stream(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	send := func() {
		writeSSEEvent(w, h.renderFragment(user.ID))
		flusher.Flush()
	}
	send()

	changes, unsubscribe := h.Store.Subscribe()
	defer unsubscribe()

	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case changedUserID := <-changes:
			if changedUserID == user.ID {
				send()
			}
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (h *NotificationsHandler) renderFragment(userID int64) string {
	var buf bytes.Buffer
	h.Tmpl.partials.ExecuteTemplate(&buf, "partials/notifications.html", notificationsViewData(h.Store.List(userID)))
	return buf.String()
}

// writeSSEEvent formats data per the SSE spec: every line prefixed with
// "data: " (a bare multi-line payload would otherwise terminate the event
// early at its first blank line, and a rendered HTML fragment is always
// multi-line).
//
// Deliberately unnamed (no "event: ..." line): htmx 4's SSE extension only
// auto-swaps unnamed messages into the connected element - a named event is
// instead dispatched as a plain DOM CustomEvent with no swap performed,
// which is a real behavior change from htmx 2's sse-swap="name" attribute
// this stream's one consumer (#notifications in layout.html) used to rely
// on. Since this stream has always carried exactly one kind of message,
// switching to unnamed is the direct replacement - see the htmx-sse
// extension's own console.warn for "sse-swap is removed in htmx 4" for the
// upstream migration guidance this follows.
func writeSSEEvent(w http.ResponseWriter, data string) {
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		fmt.Fprintf(w, "data: %s\n", scanner.Text())
	}
	fmt.Fprint(w, "\n")
}
