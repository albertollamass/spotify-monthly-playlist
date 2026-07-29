package scheduler

import (
	"context"
	"log"
	"time"

	"spotify-monthly-playlist/internal/auth"
	"spotify-monthly-playlist/internal/db"
	"spotify-monthly-playlist/internal/security"
	"spotify-monthly-playlist/internal/spotify"
)

type AutoSync struct {
	repo     *db.Repository
	oauth    *auth.SpotifyOAuthService
	spotify  *spotify.Client
	cipher   *security.TokenCipher
	interval time.Duration
	cancel   context.CancelFunc
}

func NewAutoSync(repo *db.Repository, oauth *auth.SpotifyOAuthService, spotifyClient *spotify.Client, cipher *security.TokenCipher, interval time.Duration) *AutoSync {
	return &AutoSync{repo: repo, oauth: oauth, spotify: spotifyClient, cipher: cipher, interval: interval}
}

func (a *AutoSync) Start(parent context.Context) {
	if a.interval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	a.cancel = cancel
	go func() {
		ticker := time.NewTicker(a.interval)
		defer ticker.Stop()
		a.syncOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.syncOnce(ctx)
			}
		}
	}()
}

func (a *AutoSync) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *AutoSync) syncOnce(ctx context.Context) {
	users, err := a.repo.ListUsersWithTokens(ctx)
	if err != nil {
		log.Printf("autosync: list users failed: %v", err)
		return
	}
	for _, u := range users {
		accessToken := u.AccessToken
		expiry := u.ExpiresAt
		if expiry.Before(time.Now().Add(30 * time.Second)) {
			refresh, err := a.cipher.Decrypt(u.RefreshToken)
			if err != nil {
				log.Printf("autosync: decrypt refresh token failed for user %d: %v", u.UserID, err)
				continue
			}
			newTok, err := a.oauth.Refresh(ctx, refresh)
			if err != nil {
				log.Printf("autosync: refresh token failed for user %d: %v", u.UserID, err)
				continue
			}
			encryptedRefresh, err := a.cipher.Encrypt(newTok.RefreshToken)
			if err != nil {
				log.Printf("autosync: encrypt refresh token failed for user %d: %v", u.UserID, err)
				continue
			}
			if err := a.repo.SaveSpotifyToken(ctx, u.UserID, newTok.AccessToken, encryptedRefresh, newTok.Expiry); err != nil {
				log.Printf("autosync: save refreshed token failed for user %d: %v", u.UserID, err)
				continue
			}
			accessToken = newTok.AccessToken
		}

		played, err := a.spotify.GetRecentlyPlayed(ctx, accessToken, 50)
		if err != nil {
			log.Printf("autosync: recently played failed for user %d: %v", u.UserID, err)
			continue
		}
		tracks := make([]db.SyncTrack, 0, len(played))
		for _, p := range played {
			artist := ""
			if len(p.Track.Artists) > 0 {
				artist = p.Track.Artists[0].Name
			}
			tracks = append(tracks, db.SyncTrack{
				SpotifyTrackID: p.Track.ID,
				Name:           p.Track.Name,
				ArtistName:     artist,
				PlayedAt:       p.PlayedAt,
			})
		}
		if err := a.repo.SavePlayEvents(ctx, u.UserID, tracks); err != nil {
			log.Printf("autosync: save play events failed for user %d: %v", u.UserID, err)
			continue
		}
		log.Printf("autosync: user %d synced %d items", u.UserID, len(tracks))
	}
}
