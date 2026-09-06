package handlers

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

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
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{
		"Notifications": h.Store.List(userID),
	})
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
		writeSSEEvent(w, "notification", h.renderFragment(user.ID))
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
	h.Tmpl.partials.ExecuteTemplate(&buf, "partials/notifications.html", map[string]any{
		"Notifications": h.Store.List(userID),
	})
	return buf.String()
}

// writeSSEEvent formats data per the SSE spec: every line prefixed with
// "data: " (a bare multi-line payload would otherwise terminate the event
// early at its first blank line, and a rendered HTML fragment is always
// multi-line).
func writeSSEEvent(w http.ResponseWriter, event, data string) {
	fmt.Fprintf(w, "event: %s\n", event)
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		fmt.Fprintf(w, "data: %s\n", scanner.Text())
	}
	fmt.Fprint(w, "\n")
}
