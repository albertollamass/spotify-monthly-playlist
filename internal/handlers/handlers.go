package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"spotify-monthly-playlist/internal/auth"
	"spotify-monthly-playlist/internal/config"
	"spotify-monthly-playlist/internal/db"
	"spotify-monthly-playlist/internal/security"
	"spotify-monthly-playlist/internal/spotify"
)

type Handler struct {
	repo        *db.Repository
	sessions    *security.SessionStore
	oauth       *auth.SpotifyOAuthService
	spotify     *spotify.Client
	tokenCipher *security.TokenCipher
	cfg         config.Config
	indexTmpl   *template.Template
}

type indexData struct {
	LoggedIn    bool
	SpotifyUser string
	Message     string
	CSRFToken   string
}

func New(repo *db.Repository, sessions *security.SessionStore, oauth *auth.SpotifyOAuthService, spotifyClient *spotify.Client, tokenCipher *security.TokenCipher, cfg config.Config) *Handler {
	t := template.Must(template.ParseFiles("web/templates/index.html"))
	return &Handler{repo: repo, sessions: sessions, oauth: oauth, spotify: spotifyClient, tokenCipher: tokenCipher, cfg: cfg, indexTmpl: t}
}

func (h *Handler) SessionStore() *security.SessionStore {
	return h.sessions
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	data := indexData{}
	if sess, err := h.sessions.Get(r); err == nil {
		data.LoggedIn = true
		data.SpotifyUser = sess.SpotifyUser
		data.CSRFToken = sess.CSRFToken
	}
	if msg := r.URL.Query().Get("msg"); msg != "" {
		data.Message = msg
	}
	if err := h.indexTmpl.Execute(w, data); err != nil {
		http.Error(w, "template render error", http.StatusInternalServerError)
	}
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	url, err := h.oauth.AuthCodeURL()
	if err != nil {
		http.Error(w, "failed to start oauth", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing oauth params", http.StatusBadRequest)
		return
	}

	tok, err := h.oauth.Exchange(r.Context(), code, state)
	if err != nil {
		http.Error(w, "oauth exchange failed", http.StatusUnauthorized)
		return
	}

	profile, err := h.spotify.GetCurrentUser(r.Context(), tok.AccessToken)
	if err != nil {
		http.Error(w, "unable to fetch spotify profile", http.StatusBadGateway)
		return
	}

	userID, err := h.repo.UpsertUser(r.Context(), profile.ID, profile.DisplayName)
	if err != nil {
		http.Error(w, "unable to persist user", http.StatusInternalServerError)
		return
	}

	encryptedRefresh, err := h.tokenCipher.Encrypt(tok.RefreshToken)
	if err != nil {
		http.Error(w, "unable to secure token", http.StatusInternalServerError)
		return
	}

	if err := h.repo.SaveSpotifyToken(r.Context(), userID, tok.AccessToken, encryptedRefresh, tok.Expiry); err != nil {
		http.Error(w, "unable to persist token", http.StatusInternalServerError)
		return
	}

	handoffToken, err := h.sessions.CreatePending(security.Session{
		UserID:      userID,
		SpotifyUser: profile.ID,
		AccessToken: tok.AccessToken,
		TokenExpiry: tok.Expiry,
	})
	if err != nil {
		http.Error(w, "unable to create session", http.StatusInternalServerError)
		return
	}
	target := "/auth/finalize?token=" + handoffToken
	if h.cfg.AppURL != "" {
		target = h.cfg.AppURL + target
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (h *Handler) Finalize(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	if err := h.sessions.ClaimPending(w, token); err != nil {
		http.Error(w, "invalid or expired session token", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/?msg=Login+correcto", http.StatusFound)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	sess, err := h.requireSession(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !security.ValidCSRFToken(r, sess) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	h.sessions.Destroy(w, r)
	http.Redirect(w, r, "/?msg=Sesion+cerrada", http.StatusFound)
}

func (h *Handler) SyncManual(w http.ResponseWriter, r *http.Request) {
	sess, err := h.requireSession(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !security.ValidCSRFToken(r, sess) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := h.ensureValidAccessToken(r.Context(), sess); err != nil {
		http.Error(w, "spotify auth refresh failed", http.StatusUnauthorized)
		return
	}

	tracks, err := h.spotify.GetRecentlyPlayed(r.Context(), sess.AccessToken, 50)
	if err != nil {
		http.Error(w, "spotify sync failed", http.StatusBadGateway)
		return
	}
	items := make([]db.SyncTrack, 0, len(tracks))
	for _, t := range tracks {
		if t.Track.ID == "" {
			// Ficheros locales de Spotify no tienen ID; se descartan.
			continue
		}
		artistName := ""
		if len(t.Track.Artists) > 0 {
			artistName = t.Track.Artists[0].Name
		}
		items = append(items, db.SyncTrack{
			SpotifyTrackID: t.Track.ID,
			Name:           t.Track.Name,
			ArtistName:     artistName,
			PlayedAt:       t.PlayedAt,
		})
	}

	if err := h.repo.SavePlayEvents(r.Context(), sess.UserID, items); err != nil {
		http.Error(w, "save play events failed", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"synced_items": len(items)})
}

// PreviewTracks devuelve las canciones de la BD para los meses indicados sin
// llamar a Spotify. Útil para depurar antes de crear la playlist.
func (h *Handler) PreviewTracks(w http.ResponseWriter, r *http.Request) {
	sess, err := h.requireSession(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	monthsParam := r.URL.Query().Get("months")
	if monthsParam == "" {
		http.Error(w, "months query param required", http.StatusBadRequest)
		return
	}
	var months []string
	for _, m := range strings.Split(monthsParam, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			months = append(months, m)
		}
	}
	if !monthsAreValid(months) {
		http.Error(w, "invalid month format (expected YYYY-MM)", http.StatusBadRequest)
		return
	}
	tracks, err := h.repo.TopTracksByMonths(r.Context(), sess.UserID, months, 10)
	if err != nil {
		http.Error(w, "unable to query tracks", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"total":  len(tracks),
		"tracks": tracks,
	})
}

func (h *Handler) AvailableMonths(w http.ResponseWriter, r *http.Request) {
	sess, err := h.requireSession(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := h.ensureValidAccessToken(r.Context(), sess); err != nil {
		http.Error(w, "spotify auth refresh failed", http.StatusUnauthorized)
		return
	}
	months, err := h.repo.AvailableMonths(r.Context(), sess.UserID)
	if err != nil {
		http.Error(w, "unable to list months", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"months": months})
}

type createPlaylistRequest struct {
	Months []string `json:"months"`
	Limit  int      `json:"limit"`
}

var spotifyIDRE = regexp.MustCompile(`^[A-Za-z0-9]{22}$`)

func (h *Handler) CreateMonthlyPlaylist(w http.ResponseWriter, r *http.Request) {
	sess, err := h.requireSession(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !security.ValidCSRFToken(r, sess) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := h.ensureValidAccessToken(r.Context(), sess); err != nil {
		http.Error(w, "spotify auth refresh failed", http.StatusUnauthorized)
		return
	}

	var req createPlaylistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Months) == 0 {
		http.Error(w, "months are required", http.StatusBadRequest)
		return
	}
	if !monthsAreValid(req.Months) {
		http.Error(w, "invalid month format (expected YYYY-MM)", http.StatusBadRequest)
		return
	}
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 30
	}

	queryMonths := append([]string(nil), req.Months...)
	usedApproxMonths := false
	tracks, err := h.repo.TopTracksByMonths(r.Context(), sess.UserID, queryMonths, req.Limit)
	if err != nil {
		http.Error(w, "unable to compute monthly top", http.StatusInternalServerError)
		return
	}
	if len(tracks) == 0 {
		if err := h.backfillMonthsFromTopTracks(r.Context(), sess, queryMonths); err != nil {
			if h.handleSpotifyError(w, r, "backfill monthly months", err) {
				return
			}
			log.Printf("warn: backfill monthly months failed for user=%s months=%v: %v", sess.SpotifyUser, queryMonths, err)
		}
		tracks, err = h.repo.TopTracksByMonths(r.Context(), sess.UserID, queryMonths, req.Limit)
		if err != nil {
			http.Error(w, "unable to compute monthly top", http.StatusInternalServerError)
			return
		}
	}
	if len(tracks) == 0 {
		availableMonths, monthsErr := h.repo.AvailableMonths(r.Context(), sess.UserID)
		if monthsErr != nil {
			log.Printf("warn: unable to query available months for user=%d: %v", sess.UserID, monthsErr)
		}
		resolvedMonths := resolveMonthsUsingAvailable(queryMonths, availableMonths)
		if len(resolvedMonths) > 0 {
			tracks, err = h.repo.TopTracksByMonths(r.Context(), sess.UserID, resolvedMonths, req.Limit)
			if err != nil {
				http.Error(w, "unable to compute monthly top", http.StatusInternalServerError)
				return
			}
			if len(tracks) > 0 {
				queryMonths = resolvedMonths
				usedApproxMonths = true
			}
		}
	}
	if len(tracks) == 0 {
		availableMonths, monthsErr := h.repo.AvailableMonths(r.Context(), sess.UserID)
		if monthsErr != nil {
			log.Printf("warn: unable to query available months for user=%d: %v", sess.UserID, monthsErr)
		}
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error":            "no_data_for_selected_months",
			"message":          "No hay datos para los meses seleccionados",
			"available_months": availableMonths,
			"suggested_months": resolveMonthsUsingAvailable(queryMonths, availableMonths),
		})
		return
	}

	uris := make([]string, 0, len(tracks))
	for _, t := range tracks {
		if t.SpotifyTrackID == "" {
			continue
		}
		uris = append(uris, "spotify:track:"+t.SpotifyTrackID)
	}
	if len(uris) == 0 {
		http.Error(w, "no valid tracks for selected months", http.StatusBadRequest)
		return
	}
	log.Printf("creating monthly playlist for user=%s requested_months=%v query_months=%v tracks=%d uris_sample=%v", sess.SpotifyUser, req.Months, queryMonths, len(uris), uris[:min(3, len(uris))])

	playlistName := "Monthly Top - " + strings.Join(queryMonths, ",")
	if usedApproxMonths {
		playlistName += " (aprox)"
	}
	playlistID, playlistURL, err := h.spotify.CreatePlaylist(r.Context(), sess.AccessToken, sess.SpotifyUser, playlistName, "Generada por spotify-monthly-playlist", true)
	if err != nil {
		if h.handleSpotifyError(w, r, "create playlist", err) {
			return
		}
		log.Printf("error creating spotify playlist (user=%s): %v", sess.SpotifyUser, err)
		http.Error(w, "unable to create spotify playlist", http.StatusBadGateway)
		return
	}
	log.Printf("created playlist id=%s url=%s; now adding %d tracks", playlistID, playlistURL, len(uris))

	if err := h.spotify.AddTracksToPlaylist(r.Context(), sess.AccessToken, playlistID, uris); err != nil {
		if h.handleSpotifyError(w, r, "add tracks", err) {
			return
		}
		log.Printf("error adding %d tracks to playlist %s: %v", len(uris), playlistID, err)
		http.Error(w, "unable to add tracks", http.StatusBadGateway)
		return
	}
	log.Printf("successfully added %d tracks to playlist %s", len(uris), playlistID)

	if err := h.repo.RecordGeneratedPlaylist(r.Context(), sess.UserID, playlistID, "monthly", queryMonths); err != nil {
		log.Printf("warn: failed recording generated playlist: %v", err)
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"playlist_id":        playlistID,
		"playlist_url":       playlistURL,
		"tracks_added":       len(uris),
		"requested_months":   req.Months,
		"resolved_months":    queryMonths,
		"approximation_used": usedApproxMonths,
	})
}

func (h *Handler) CreateRecommendationsPlaylist(w http.ResponseWriter, r *http.Request) {
	sess, err := h.requireSession(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !security.ValidCSRFToken(r, sess) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := h.ensureValidAccessToken(r.Context(), sess); err != nil {
		http.Error(w, "spotify auth refresh failed", http.StatusUnauthorized)
		return
	}

	var req createPlaylistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Months) == 0 {
		http.Error(w, "months are required", http.StatusBadRequest)
		return
	}
	if !monthsAreValid(req.Months) {
		http.Error(w, "invalid month format (expected YYYY-MM)", http.StatusBadRequest)
		return
	}
	queryMonths := append([]string(nil), req.Months...)
	usedApproxMonths := false
	seedTracks, err := h.repo.TopTracksByMonths(r.Context(), sess.UserID, queryMonths, 30)
	if err != nil {
		http.Error(w, "unable to compute seeds", http.StatusInternalServerError)
		return
	}
	if len(seedTracks) == 0 {
		if err := h.backfillMonthsFromTopTracks(r.Context(), sess, queryMonths); err != nil {
			if h.handleSpotifyError(w, r, "backfill recommendations months", err) {
				return
			}
			log.Printf("warn: backfill recommendations months failed for user=%s months=%v: %v", sess.SpotifyUser, queryMonths, err)
		}
		seedTracks, err = h.repo.TopTracksByMonths(r.Context(), sess.UserID, queryMonths, 5)
		if err != nil {
			http.Error(w, "unable to compute seeds", http.StatusInternalServerError)
			return
		}
	}
	if len(seedTracks) == 0 {
		availableMonths, monthsErr := h.repo.AvailableMonths(r.Context(), sess.UserID)
		if monthsErr != nil {
			log.Printf("warn: unable to query available months for user=%d: %v", sess.UserID, monthsErr)
		}
		resolvedMonths := resolveMonthsUsingAvailable(queryMonths, availableMonths)
		if len(resolvedMonths) > 0 {
			seedTracks, err = h.repo.TopTracksByMonths(r.Context(), sess.UserID, resolvedMonths, 5)
			if err != nil {
				http.Error(w, "unable to compute seeds", http.StatusInternalServerError)
				return
			}
			if len(seedTracks) > 0 {
				queryMonths = resolvedMonths
				usedApproxMonths = true
			}
		}
	}
	if len(seedTracks) == 0 {
		availableMonths, monthsErr := h.repo.AvailableMonths(r.Context(), sess.UserID)
		if monthsErr != nil {
			log.Printf("warn: unable to query available months for user=%d: %v", sess.UserID, monthsErr)
		}
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error":            "no_data_for_selected_months",
			"message":          "No hay datos para los meses seleccionados",
			"available_months": availableMonths,
			"suggested_months": resolveMonthsUsingAvailable(queryMonths, availableMonths),
		})
		return
	}
	monthKey := strings.Join(req.Months, ",")
	seedIDs := pickSeedIDsFromMonthlyTracks(seedTracks, 5, monthKey)
	seenSeedIDs := make(map[string]struct{}, len(seedIDs))
	for _, id := range seedIDs {
		seenSeedIDs[id] = struct{}{}
	}

	if len(seedIDs) == 0 {
		topTracks, topErr := h.spotify.GetTopTracks(r.Context(), sess.AccessToken, "short_term", 5)
		if topErr != nil {
			log.Printf("warn: could not fetch fallback top tracks for recommendations user=%s: %v", sess.SpotifyUser, topErr)
		} else {
			fallbackSeedIDs := pickSeedIDsFromTopTracks(topTracks, 5, monthKey)
			for _, id := range fallbackSeedIDs {
				if _, exists := seenSeedIDs[id]; exists {
					continue
				}
				seenSeedIDs[id] = struct{}{}
				seedIDs = append(seedIDs, id)
				if len(seedIDs) == 5 {
					break
				}
			}
		}
	}

	if len(seedIDs) == 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error":   "no_valid_seed_tracks",
			"message": "No hay canciones semilla validas para pedir recomendaciones",
		})
		return
	}

	recUris, err := h.spotify.GetRecommendations(r.Context(), sess.AccessToken, seedIDs, 30)
	if err != nil {
		if h.handleSpotifyError(w, r, "get recommendations", err) {
			return
		}
		log.Printf("error fetching recommendations: %v", err)
		jsonResponse(w, http.StatusBadGateway, map[string]any{
			"error":   "unable_to_fetch_recommendations",
			"message": "No se pudieron obtener recomendaciones de Spotify",
			"details": err.Error(),
		})
		return
	}
	heardTrackIDs, err := h.repo.UserHeardTrackIDs(r.Context(), sess.UserID)
	if err != nil {
		log.Printf("warn: unable to query heard tracks for user=%d: %v", sess.UserID, err)
	}
	heardSet := make(map[string]struct{}, len(heardTrackIDs)+len(seedIDs))
	for _, id := range heardTrackIDs {
		heardSet[id] = struct{}{}
	}
	for _, seedID := range seedIDs {
		heardSet[seedID] = struct{}{}
	}

	recUris = filterUnheardTrackURIs(recUris, heardSet)
	if len(recUris) < 30 {
		fallbackLimit := 30 - len(recUris)
		fallbackTracks, fbErr := h.collectTopTracksForMonth(r.Context(), sess, queryMonths, fallbackLimit*2)
		if fbErr != nil {
			log.Printf("warn: fallback recommendations from top tracks failed user=%s: %v", sess.SpotifyUser, fbErr)
		} else {
			fallbackUris := make([]string, 0, len(fallbackTracks))
			for _, t := range fallbackTracks {
				if t.URI == "" {
					continue
				}
				fallbackUris = append(fallbackUris, t.URI)
			}
			fallbackUris = filterUnheardTrackURIs(fallbackUris, heardSet)
			for _, uri := range fallbackUris {
				recUris = append(recUris, uri)
				if len(recUris) == 30 {
					break
				}
			}
			_ = fallbackLimit
		}
	}

	if len(recUris) == 0 {
		http.Error(w, "spotify returned no recommendations", http.StatusBadGateway)
		return
	}

	playlistName := "Recommendations - " + strings.Join(queryMonths, ",")
	if usedApproxMonths {
		playlistName += " (aprox)"
	}
	playlistID, playlistURL, err := h.spotify.CreatePlaylist(r.Context(), sess.AccessToken, sess.SpotifyUser, playlistName, "Recomendaciones basadas en tu historial", true)
	if err != nil {
		if h.handleSpotifyError(w, r, "create recommendations playlist", err) {
			return
		}
		log.Printf("error creating recommendations playlist (user=%s): %v", sess.SpotifyUser, err)
		http.Error(w, "unable to create spotify playlist", http.StatusBadGateway)
		return
	}
	if err := h.spotify.AddTracksToPlaylist(r.Context(), sess.AccessToken, playlistID, recUris); err != nil {
		if h.handleSpotifyError(w, r, "add recommendations", err) {
			return
		}
		log.Printf("error adding recommendations to playlist %s: %v", playlistID, err)
		http.Error(w, "unable to add recommendations", http.StatusBadGateway)
		return
	}
	if err := h.repo.RecordGeneratedPlaylist(r.Context(), sess.UserID, playlistID, "recommendations", queryMonths); err != nil {
		log.Printf("warn: failed recording generated playlist: %v", err)
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"playlist_id":        playlistID,
		"playlist_url":       playlistURL,
		"tracks_added":       len(recUris),
		"requested_months":   req.Months,
		"resolved_months":    queryMonths,
		"approximation_used": usedApproxMonths,
	})
}

func (h *Handler) requireSession(r *http.Request) (*security.Session, error) {
	sess, err := h.sessions.Get(r)
	if err != nil {
		return nil, err
	}
	if sess.AccessToken == "" {
		return nil, errors.New("missing access token")
	}
	return sess, nil
}

func (h *Handler) ensureValidAccessToken(ctx context.Context, sess *security.Session) error {
	if sess.TokenExpiry.After(time.Now().Add(30*time.Second)) && sess.AccessToken != "" {
		return nil
	}

	rec, err := h.repo.GetSpotifyToken(ctx, sess.UserID)
	if err != nil {
		return err
	}
	if rec.ExpiresAt.After(time.Now().Add(30*time.Second)) && rec.AccessToken != "" {
		sess.AccessToken = rec.AccessToken
		sess.TokenExpiry = rec.ExpiresAt
		h.sessions.Update(*sess)
		return nil
	}

	plainRefresh, err := h.tokenCipher.Decrypt(rec.RefreshToken)
	if err != nil {
		return err
	}
	newToken, err := h.oauth.Refresh(ctx, plainRefresh)
	if err != nil {
		return err
	}
	encryptedRefresh, err := h.tokenCipher.Encrypt(newToken.RefreshToken)
	if err != nil {
		return err
	}
	if err := h.repo.SaveSpotifyToken(ctx, sess.UserID, newToken.AccessToken, encryptedRefresh, newToken.Expiry); err != nil {
		return err
	}
	sess.AccessToken = newToken.AccessToken
	sess.TokenExpiry = newToken.Expiry
	h.sessions.Update(*sess)
	return nil
}

func monthsAreValid(months []string) bool {
	monthRE := regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
	for _, m := range months {
		if !monthRE.MatchString(strings.TrimSpace(m)) {
			return false
		}
	}
	return true
}

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("json encode failed: %v", err)
	}
}

// handleSpotifyError checks if the Spotify error is a 403 (missing scopes) or
// 401 (expired/invalid token). If so it destroys the session so the user is
// forced to re-authorise, and writes a 401 response with a JSON message the
// frontend can act on.
// Returns true when the error has been handled (caller must stop processing).
func (h *Handler) handleSpotifyError(w http.ResponseWriter, r *http.Request, op string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, spotify.ErrForbidden) || errors.Is(err, spotify.ErrUnauthorized) {
		log.Printf("spotify reauth required on %s (%v) — destroying session to force re-login", op, err)
		h.sessions.Destroy(w, r)
		jsonResponse(w, http.StatusUnauthorized, map[string]string{
			"error":    "spotify_reauth_required",
			"message":  "Tu sesión de Spotify ha caducado o le faltan permisos. Por favor vuelve a conectarte.",
			"redirect": "/auth/login",
		})
		return true
	}
	log.Printf("spotify error on %s: %v", op, err)
	return false
}

func WithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 10*time.Second)
}

func monthTimeRange(month string, now time.Time) string {
	targetMonth, err := time.ParseInLocation("2006-01", strings.TrimSpace(month), now.Location())
	if err != nil {
		return "long_term"
	}
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	target := time.Date(targetMonth.Year(), targetMonth.Month(), 1, 0, 0, 0, 0, now.Location())
	delta := (current.Year()-target.Year())*12 + int(current.Month()-target.Month())
	if delta <= 1 {
		return "short_term"
	}
	if delta <= 6 {
		return "medium_term"
	}
	return "long_term"
}

func (h *Handler) backfillMonthsFromTopTracks(ctx context.Context, sess *security.Session, months []string) error {
	now := time.Now()
	for _, month := range months {
		month = strings.TrimSpace(month)
		if month == "" {
			continue
		}

		existing, err := h.repo.TopTracksByMonths(ctx, sess.UserID, []string{month}, 1)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			continue
		}

		topTracks, err := h.collectTopTracksForMonth(ctx, sess, []string{month}, 50)
		if err != nil {
			return err
		}
		topTracks = rotateTopTracksByMonth(topTracks, month)

		monthStart, err := time.ParseInLocation("2006-01", month, now.Location())
		if err != nil {
			continue
		}
		startOfMonth := time.Date(monthStart.Year(), monthStart.Month(), 1, 0, 0, 0, 0, now.Location())

		items := make([]db.SyncTrack, 0, len(topTracks)*3)
		for i, t := range topTracks {
			if !spotifyIDRE.MatchString(strings.TrimSpace(t.ID)) {
				continue
			}
			artistName := ""
			if len(t.Artists) > 0 {
				artistName = t.Artists[0].Name
			}
			for replay := 0; replay < syntheticPlayWeight(i, month); replay++ {
				playedAt := startOfMonth.Add(time.Duration(i*10+replay) * time.Minute)
				items = append(items, db.SyncTrack{
					SpotifyTrackID: t.ID,
					Name:           t.Name,
					ArtistName:     artistName,
					PlayedAt:       playedAt,
				})
			}
		}
		if len(items) == 0 {
			continue
		}
		if err := h.repo.SavePlayEvents(ctx, sess.UserID, items); err != nil {
			return err
		}
		log.Printf("backfilled month=%s for user=%s with %d items", month, sess.SpotifyUser, len(items))
	}
	return nil
}

func (h *Handler) collectTopTracksForMonth(ctx context.Context, sess *security.Session, months []string, limit int) ([]spotify.TopTrackItem, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 50 {
		limit = 50
	}

	ranges := orderedTimeRanges(months)
	seenIDs := make(map[string]struct{})
	out := make([]spotify.TopTrackItem, 0, limit)

	for _, timeRange := range ranges {
		topTracks, err := h.spotify.GetTopTracks(ctx, sess.AccessToken, timeRange, 50)
		if err != nil {
			continue
		}
		for _, t := range topTracks {
			id := strings.TrimSpace(t.ID)
			if !spotifyIDRE.MatchString(id) {
				continue
			}
			if _, exists := seenIDs[id]; exists {
				continue
			}
			seenIDs[id] = struct{}{}
			out = append(out, t)
			if len(out) == limit {
				return out, nil
			}
		}
	}

	recentlyPlayed, err := h.spotify.GetRecentlyPlayed(ctx, sess.AccessToken, 50)
	if err == nil {
		for _, rp := range recentlyPlayed {
			id := strings.TrimSpace(rp.Track.ID)
			if !spotifyIDRE.MatchString(id) {
				continue
			}
			if _, exists := seenIDs[id]; exists {
				continue
			}
			seenIDs[id] = struct{}{}
			artistName := ""
			if len(rp.Track.Artists) > 0 {
				artistName = rp.Track.Artists[0].Name
			}
			out = append(out, spotify.TopTrackItem{
				ID:   id,
				URI:  "spotify:track:" + id,
				Name: rp.Track.Name,
				Artists: []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				}{
					{ID: "", Name: artistName},
				},
			})
			if len(out) == limit {
				return out, nil
			}
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no top tracks available for selected months")
	}
	return out, nil
}

func orderedTimeRanges(months []string) []string {
	now := time.Now()
	seen := map[string]struct{}{}
	out := make([]string, 0, 3)
	for _, month := range months {
		timeRange := monthTimeRange(month, now)
		if _, exists := seen[timeRange]; exists {
			continue
		}
		seen[timeRange] = struct{}{}
		out = append(out, timeRange)
	}
	for _, fallback := range []string{"short_term", "medium_term", "long_term"} {
		if _, exists := seen[fallback]; exists {
			continue
		}
		out = append(out, fallback)
	}
	return out
}

func resolveMonthsUsingAvailable(requestedMonths, availableMonths []string) []string {
	if len(requestedMonths) == 0 || len(availableMonths) == 0 {
		return nil
	}
	availableSet := make(map[string]struct{}, len(availableMonths))
	for _, month := range availableMonths {
		month = strings.TrimSpace(month)
		if month == "" {
			continue
		}
		availableSet[month] = struct{}{}
	}

	out := make([]string, 0, len(requestedMonths))
	seen := make(map[string]struct{}, len(requestedMonths))
	for _, month := range requestedMonths {
		month = strings.TrimSpace(month)
		if month == "" {
			continue
		}
		chosen := month
		if _, exists := availableSet[month]; !exists {
			chosen = nearestAvailableMonth(month, availableMonths)
			if chosen == "" {
				continue
			}
		}
		if _, exists := seen[chosen]; exists {
			continue
		}
		seen[chosen] = struct{}{}
		out = append(out, chosen)
	}
	return out
}

func nearestAvailableMonth(target string, candidates []string) string {
	targetTime, err := time.Parse("2006-01", strings.TrimSpace(target))
	if err != nil {
		return ""
	}
	best := ""
	bestDistance := int(^uint(0) >> 1)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		candidateTime, err := time.Parse("2006-01", candidate)
		if err != nil {
			continue
		}
		delta := monthDistance(targetTime, candidateTime)
		if delta < bestDistance {
			best = candidate
			bestDistance = delta
			continue
		}
		if delta == bestDistance && candidate > best {
			best = candidate
		}
	}
	return best
}

func monthDistance(a, b time.Time) int {
	months := (a.Year()-b.Year())*12 + int(a.Month()-b.Month())
	if months < 0 {
		return -months
	}
	return months
}

func monthHash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.TrimSpace(s)))
	return h.Sum32()
}

func rotateTopTracksByMonth(tracks []spotify.TopTrackItem, month string) []spotify.TopTrackItem {
	if len(tracks) <= 1 {
		return tracks
	}
	offset := int(monthHash(month) % uint32(len(tracks)))
	if offset == 0 {
		return tracks
	}
	out := make([]spotify.TopTrackItem, 0, len(tracks))
	out = append(out, tracks[offset:]...)
	out = append(out, tracks[:offset]...)
	return out
}

func syntheticPlayWeight(index int, month string) int {
	base := 1
	switch {
	case index < 8:
		base = 4
	case index < 20:
		base = 3
	case index < 35:
		base = 2
	}
	// Deterministic month-specific boost to avoid identical rankings in old months.
	if ((index + int(monthHash(month)%17)) % 7) == 0 {
		base++
	}
	if base > 5 {
		return 5
	}
	return base
}

func pickSeedIDsFromMonthlyTracks(tracks []db.MonthlyTrack, max int, monthKey string) []string {
	if max <= 0 || len(tracks) == 0 {
		return nil
	}
	offset := int(monthHash(monthKey) % uint32(len(tracks)))
	seedIDs := make([]string, 0, max)
	seen := make(map[string]struct{}, max)
	for i := 0; i < len(tracks) && len(seedIDs) < max; i++ {
		idx := (offset + i) % len(tracks)
		id := strings.TrimSpace(tracks[idx].SpotifyTrackID)
		if !spotifyIDRE.MatchString(id) {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		seedIDs = append(seedIDs, id)
	}
	return seedIDs
}

func pickSeedIDsFromTopTracks(tracks []spotify.TopTrackItem, max int, monthKey string) []string {
	if max <= 0 || len(tracks) == 0 {
		return nil
	}
	offset := int(monthHash(monthKey) % uint32(len(tracks)))
	seedIDs := make([]string, 0, max)
	seen := make(map[string]struct{}, max)
	for i := 0; i < len(tracks) && len(seedIDs) < max; i++ {
		idx := (offset + i) % len(tracks)
		id := strings.TrimSpace(tracks[idx].ID)
		if !spotifyIDRE.MatchString(id) {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		seedIDs = append(seedIDs, id)
	}
	return seedIDs
}

func trackIDFromURI(uri string) string {
	const prefix = "spotify:track:"
	if !strings.HasPrefix(uri, prefix) {
		return ""
	}
	id := strings.TrimSpace(strings.TrimPrefix(uri, prefix))
	if !spotifyIDRE.MatchString(id) {
		return ""
	}
	return id
}

func filterUnheardTrackURIs(uris []string, heardSet map[string]struct{}) []string {
	if len(uris) == 0 {
		return nil
	}
	seenURIs := make(map[string]struct{}, len(uris))
	out := make([]string, 0, len(uris))
	for _, uri := range uris {
		trackID := trackIDFromURI(uri)
		if trackID == "" {
			continue
		}
		if _, heard := heardSet[trackID]; heard {
			continue
		}
		if _, exists := seenURIs[uri]; exists {
			continue
		}
		seenURIs[uri] = struct{}{}
		out = append(out, uri)
	}
	return out
}

// TestPlaylist crea una playlist de prueba con un track hardcodeado para
// verificar que los scopes de Spotify están correctamente concedidos.
func (h *Handler) TestPlaylist(w http.ResponseWriter, r *http.Request) {
	sess, err := h.requireSession(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !security.ValidCSRFToken(r, sess) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := h.ensureValidAccessToken(r.Context(), sess); err != nil {
		http.Error(w, "spotify auth refresh failed", http.StatusUnauthorized)
		return
	}

	playlistID, playlistURL, err := h.spotify.CreatePlaylist(r.Context(), sess.AccessToken, sess.SpotifyUser, "TEST - scope check", "Playlist de prueba", true)
	if err != nil {
		if h.handleSpotifyError(w, r, "test create playlist", err) {
			return
		}
		http.Error(w, "create playlist failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	log.Printf("test playlist created id=%s", playlistID)

	testURI := "spotify:track:16V16vOFivfaGnOAc1jzAb"
	if topTracks, topErr := h.spotify.GetTopTracks(r.Context(), sess.AccessToken, "short_term", 1); topErr == nil && len(topTracks) > 0 && topTracks[0].ID != "" {
		testURI = "spotify:track:" + topTracks[0].ID
	}
	if err := h.spotify.AddTracksToPlaylist(r.Context(), sess.AccessToken, playlistID, []string{testURI}); err != nil {
		if h.handleSpotifyError(w, r, "test add track", err) {
			return
		}
		log.Printf("test add track failed: %v", err)
		http.Error(w, "add track failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	log.Printf("test track added successfully to playlist %s", playlistID)
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":           true,
		"playlist_id":  playlistID,
		"playlist_url": playlistURL,
		"track_uri":    testURI,
	})
}

// SyncTopTracks importa las top tracks de Spotify (short_term ≈ últimas 4 semanas)
// y las guarda como play events en el mes actual. Útil para arrancar con datos
// de "más escuchadas" sin necesidad de haber sincronizado historial durante todo el mes.
func (h *Handler) SyncTopTracks(w http.ResponseWriter, r *http.Request) {
	sess, err := h.requireSession(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !security.ValidCSRFToken(r, sess) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := h.ensureValidAccessToken(r.Context(), sess); err != nil {
		http.Error(w, "spotify auth refresh failed", http.StatusUnauthorized)
		return
	}

	topTracks, err := h.spotify.GetTopTracks(r.Context(), sess.AccessToken, "short_term", 50)
	if err != nil {
		if h.handleSpotifyError(w, r, "get top tracks", err) {
			return
		}
		http.Error(w, "get top tracks failed", http.StatusBadGateway)
		return
	}

	// Distribuimos las pistas con timestamps dentro del mes actual para que
	// aparezcan en la consulta de TopTracksByMonths. El rango va desde el
	// inicio del mes hasta ahora, con cada pista 1 minuto separada de la anterior
	// para respetar el UNIQUE(user_id, spotify_track_id, played_at).
	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	items := make([]db.SyncTrack, 0, len(topTracks))
	for i, t := range topTracks {
		if t.ID == "" {
			continue
		}
		artistName := ""
		if len(t.Artists) > 0 {
			artistName = t.Artists[0].Name
		}
		// played_at dentro del mes actual, separadas 1 minuto entre sí
		playedAt := startOfMonth.Add(time.Duration(i) * time.Minute)
		items = append(items, db.SyncTrack{
			SpotifyTrackID: t.ID,
			Name:           t.Name,
			ArtistName:     artistName,
			PlayedAt:       playedAt,
		})
	}

	if err := h.repo.SavePlayEvents(r.Context(), sess.UserID, items); err != nil {
		http.Error(w, "save top tracks failed", http.StatusInternalServerError)
		return
	}
	log.Printf("synced %d top tracks for user=%s", len(items), sess.SpotifyUser)
	jsonResponse(w, http.StatusOK, map[string]any{"synced_top_tracks": len(items)})
}
