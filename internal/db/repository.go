package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

type SyncTrack struct {
	SpotifyTrackID string
	Name           string
	ArtistName     string
	PlayedAt       time.Time
}

type MonthlyTrack struct {
	SpotifyTrackID string `json:"spotify_track_id"`
	Name           string `json:"name"`
	ArtistName     string `json:"artist_name"`
	PlayCount      int    `json:"play_count"`
}

type SpotifyTokenRecord struct {
	UserID       int64
	SpotifyUser  string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) UpsertUser(ctx context.Context, spotifyUserID, displayName string) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users(spotify_user_id, display_name)
		VALUES ($1, $2)
		ON CONFLICT(spotify_user_id)
		DO UPDATE SET display_name = EXCLUDED.display_name, updated_at = NOW()
		RETURNING id
	`, spotifyUserID, displayName).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert user: %w", err)
	}
	return id, nil
}

func (r *Repository) SaveSpotifyToken(ctx context.Context, userID int64, accessToken, refreshToken string, expiry time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO spotify_tokens(user_id, access_token, refresh_token, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(user_id)
		DO UPDATE SET access_token = EXCLUDED.access_token,
		              refresh_token = EXCLUDED.refresh_token,
		              expires_at = EXCLUDED.expires_at,
		              updated_at = NOW()
	`, userID, accessToken, refreshToken, expiry)
	if err != nil {
		return fmt.Errorf("save spotify token: %w", err)
	}
	return nil
}

func (r *Repository) GetSpotifyToken(ctx context.Context, userID int64) (SpotifyTokenRecord, error) {
	var rec SpotifyTokenRecord
	err := r.pool.QueryRow(ctx, `
		SELECT st.user_id, u.spotify_user_id, st.access_token, st.refresh_token, st.expires_at
		FROM spotify_tokens st
		JOIN users u ON u.id = st.user_id
		WHERE st.user_id = $1
	`, userID).Scan(&rec.UserID, &rec.SpotifyUser, &rec.AccessToken, &rec.RefreshToken, &rec.ExpiresAt)
	if err != nil {
		return SpotifyTokenRecord{}, fmt.Errorf("get spotify token: %w", err)
	}
	return rec, nil
}

func (r *Repository) ListUsersWithTokens(ctx context.Context) ([]SpotifyTokenRecord, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT st.user_id, u.spotify_user_id, st.access_token, st.refresh_token, st.expires_at
		FROM spotify_tokens st
		JOIN users u ON u.id = st.user_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list users with tokens: %w", err)
	}
	defer rows.Close()

	var out []SpotifyTokenRecord
	for rows.Next() {
		var rec SpotifyTokenRecord
		if err := rows.Scan(&rec.UserID, &rec.SpotifyUser, &rec.AccessToken, &rec.RefreshToken, &rec.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan users with tokens: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users with tokens: %w", err)
	}
	return out, nil
}

func (r *Repository) SavePlayEvents(ctx context.Context, userID int64, tracks []SyncTrack) error {
	if len(tracks) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, t := range tracks {
		batch.Queue(`
			INSERT INTO play_events(user_id, spotify_track_id, track_name, artist_name, played_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT(user_id, spotify_track_id, played_at) DO NOTHING
		`, userID, t.SpotifyTrackID, t.Name, t.ArtistName, t.PlayedAt)
	}
	res := r.pool.SendBatch(ctx, batch)
	defer res.Close()
	for range tracks {
		if _, err := res.Exec(); err != nil {
			return fmt.Errorf("save play events batch: %w", err)
		}
	}
	return nil
}

func (r *Repository) AvailableMonths(ctx context.Context, userID int64) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT to_char(date_trunc('month', played_at), 'YYYY-MM') AS month
		FROM play_events
		WHERE user_id = $1
		ORDER BY month DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("query available months: %w", err)
	}
	defer rows.Close()
	months := make([]string, 0)
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("scan month: %w", err)
		}
		months = append(months, m)
	}
	return months, rows.Err()
}

func (r *Repository) TopTracksByMonths(ctx context.Context, userID int64, months []string, limit int) ([]MonthlyTrack, error) {
	if len(months) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT spotify_track_id, track_name, artist_name, COUNT(*) AS play_count
		FROM play_events
		WHERE user_id = $1
		  AND to_char(date_trunc('month', played_at), 'YYYY-MM') = ANY($2)
		GROUP BY spotify_track_id, track_name, artist_name
		ORDER BY play_count DESC, track_name ASC
		LIMIT $3
	`, userID, months, limit)
	if err != nil {
		return nil, fmt.Errorf("query top tracks by months: %w", err)
	}
	defer rows.Close()
	tracks := make([]MonthlyTrack, 0)
	for rows.Next() {
		var t MonthlyTrack
		if err := rows.Scan(&t.SpotifyTrackID, &t.Name, &t.ArtistName, &t.PlayCount); err != nil {
			return nil, fmt.Errorf("scan monthly track: %w", err)
		}
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

func (r *Repository) UserHeardTrackIDs(ctx context.Context, userID int64) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT spotify_track_id
		FROM play_events
		WHERE user_id = $1
		  AND spotify_track_id <> ''
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("query heard track ids: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan heard track id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate heard track ids: %w", err)
	}
	return ids, nil
}

func (r *Repository) RecordGeneratedPlaylist(ctx context.Context, userID int64, spotifyPlaylistID, playlistType string, selectedMonths []string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO generated_playlists(user_id, spotify_playlist_id, playlist_type, selected_months)
		VALUES ($1, $2, $3, $4)
	`, userID, spotifyPlaylistID, playlistType, strings.Join(selectedMonths, ","))
	if err != nil {
		return fmt.Errorf("record generated playlist: %w", err)
	}
	return nil
}
