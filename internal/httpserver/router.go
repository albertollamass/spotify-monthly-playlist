package httpserver

import (
	"net/http"

	"spotify-monthly-playlist/internal/handlers"
	"spotify-monthly-playlist/internal/security"
)

func NewRouter(h *handlers.Handler, secCfg security.Config) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("GET /", h.Index)
	mux.HandleFunc("GET /auth/login", h.Login)
	mux.HandleFunc("GET /auth/callback", h.Callback)
	mux.HandleFunc("GET /auth/finalize", h.Finalize)
	mux.HandleFunc("POST /auth/logout", h.Logout)
	mux.HandleFunc("POST /sync/manual", h.SyncManual)
	mux.HandleFunc("GET /api/months", h.AvailableMonths)
	mux.HandleFunc("GET /api/tracks/preview", h.PreviewTracks)
	mux.HandleFunc("POST /api/playlists/monthly", h.CreateMonthlyPlaylist)
	mux.HandleFunc("POST /api/playlists/recommendations", h.CreateRecommendationsPlaylist)
	mux.HandleFunc("POST /api/playlists/test", h.TestPlaylist)
	mux.HandleFunc("POST /sync/top-tracks", h.SyncTopTracks)

	handler := logging(recoverPanic(securityHeaders(mux)))
	return rateLimit(h.SessionStore(), handler)
}

// NewCallbackRouter returns a minimal HTTP router that only handles the
// Spotify OAuth callback. Used when the main server runs on TLS and Spotify
// requires a plain http://localhost redirect URI.
func NewCallbackRouter(h *handlers.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback", h.Callback)
	return logging(recoverPanic(mux))
}
