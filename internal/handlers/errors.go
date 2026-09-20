package handlers

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recoverer replaces chi/middleware.Recoverer with one that renders the
// themed error page (DESIGN.md §10) instead of chi's plain-text 500, while
// keeping the same "log the panic + stack trace, then answer 500" behavior.
// Registered in cmd/server/main.go in place of middleware.Recoverer.
func Recoverer(tmpl *Templates) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rvr := recover(); rvr != nil {
					slog.Error("panic recovered", "error", rvr, "path", r.URL.Path, "stack", string(debug.Stack()))
					tmpl.RenderErrorPage(w, r, http.StatusInternalServerError, "Something went wrong",
						"An unexpected error occurred. Try again, or head back home.")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
