package spotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const apiBaseURL = "https://api.spotify.com/v1"

// ErrForbidden is returned when Spotify responds with 403 Forbidden.
// This can mean missing scope, app restrictions, or ownership issues.
var ErrForbidden = fmt.Errorf("spotify returned 403 forbidden — check app permissions or re-login")

// ErrUnauthorized is returned when Spotify responds with 401, indicating the
// access token is expired or invalid. The user must re-authorise.
var ErrUnauthorized = fmt.Errorf("spotify token expired or invalid, please re-login")

type Client struct {
	httpClient *http.Client
}

type UserProfile struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type RecentlyPlayedItem struct {
	PlayedAt time.Time `json:"played_at"`
	Track    struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
	} `json:"track"`
}

type recentlyPlayedResponse struct {
	Items []RecentlyPlayedItem `json:"items"`
}

type RecommendationsResponse struct {
	Tracks []struct {
		URI string `json:"uri"`
	} `json:"tracks"`
}

func NewClient() *Client {
	return &Client{httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) GetCurrentUser(ctx context.Context, accessToken string) (UserProfile, error) {
	var p UserProfile
	err := c.doJSON(ctx, accessToken, http.MethodGet, apiBaseURL+"/me", nil, &p)
	if err != nil {
		return UserProfile{}, err
	}
	return p, nil
}

func (c *Client) GetRecentlyPlayed(ctx context.Context, accessToken string, limit int) ([]RecentlyPlayedItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	endpoint := fmt.Sprintf("%s/me/player/recently-played?limit=%d", apiBaseURL, limit)
	var resp recentlyPlayedResponse
	if err := c.doJSON(ctx, accessToken, http.MethodGet, endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func (c *Client) CreatePlaylist(ctx context.Context, accessToken, userID, name, description string, private bool) (string, string, error) {
	payload := map[string]any{
		"name":        name,
		"description": description,
		"public":      !private,
	}
	endpoint := apiBaseURL + "/me/playlists"
	var resp struct {
		ID           string `json:"id"`
		ExternalURLs struct {
			Spotify string `json:"spotify"`
		} `json:"external_urls"`
	}
	if err := c.doJSON(ctx, accessToken, http.MethodPost, endpoint, payload, &resp); err != nil {
		return "", "", err
	}
	return resp.ID, resp.ExternalURLs.Spotify, nil
}

func (c *Client) AddTracksToPlaylist(ctx context.Context, accessToken, playlistID string, uris []string) error {
	for i := 0; i < len(uris); i += 100 {
		end := i + 100
		if end > len(uris) {
			end = len(uris)
		}
		endpoint := fmt.Sprintf("%s/playlists/%s/items", apiBaseURL, url.PathEscape(playlistID))
		payload := map[string]any{"uris": uris[i:end]}
		var addResp struct {
			SnapshotID string `json:"snapshot_id"`
		}
		if err := c.doJSON(ctx, accessToken, http.MethodPost, endpoint, payload, &addResp); err != nil {
			return fmt.Errorf("add tracks batch %d-%d to playlist %s: %w", i, end, playlistID, err)
		}
		if addResp.SnapshotID == "" {
			return fmt.Errorf("spotify did not return snapshot_id for playlist %s (batch %d-%d)", playlistID, i, end)
		}
	}
	return nil
}

type TopTrackItem struct {
	ID      string `json:"id"`
	URI     string `json:"uri"`
	Name    string `json:"name"`
	Artists []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"artists"`
}

type topTracksResponse struct {
	Items []TopTrackItem `json:"items"`
}

func (c *Client) GetTopTracks(ctx context.Context, accessToken string, timeRange string, limit int) ([]TopTrackItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	endpoint := fmt.Sprintf("%s/me/top/tracks?time_range=%s&limit=%d", apiBaseURL, url.QueryEscape(timeRange), limit)
	var resp topTracksResponse
	if err := c.doJSON(ctx, accessToken, http.MethodGet, endpoint, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

func (c *Client) GetRecommendations(ctx context.Context, accessToken string, seedTrackIDs []string, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if len(seedTrackIDs) == 0 {
		return nil, fmt.Errorf("no seed tracks provided")
	}
	if len(seedTrackIDs) > 5 {
		seedTrackIDs = seedTrackIDs[:5]
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("market", "from_token")
	q.Set("seed_tracks", joinCSV(seedTrackIDs))
	endpoint := fmt.Sprintf("%s/recommendations?%s", apiBaseURL, q.Encode())

	var resp RecommendationsResponse
	if err := c.doJSON(ctx, accessToken, http.MethodGet, endpoint, nil, &resp); err != nil {
		fallback := c.fallbackRecommendationsFromTopTracks(ctx, accessToken, seedTrackIDs, limit)
		if len(fallback) > 0 {
			return fallback, nil
		}
		return nil, fmt.Errorf("recommendations endpoint failed: %w", err)
	}
	uris := make([]string, 0, len(resp.Tracks))
	for _, t := range resp.Tracks {
		if t.URI != "" {
			uris = append(uris, t.URI)
		}
	}
	if len(uris) == 0 {
		fallback := c.fallbackRecommendationsFromTopTracks(ctx, accessToken, seedTrackIDs, limit)
		if len(fallback) > 0 {
			return fallback, nil
		}
	}
	return uris, nil
}

func (c *Client) fallbackRecommendationsFromTopTracks(ctx context.Context, accessToken string, seedTrackIDs []string, limit int) []string {
	topTracks, err := c.GetTopTracks(ctx, accessToken, "short_term", 50)
	if err != nil {
		return nil
	}
	seedSet := make(map[string]struct{}, len(seedTrackIDs))
	for _, id := range seedTrackIDs {
		seedSet[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(topTracks))
	uris := make([]string, 0, limit)
	for _, t := range topTracks {
		if t.URI == "" {
			continue
		}
		if _, isSeed := seedSet[t.ID]; isSeed {
			continue
		}
		if _, exists := seen[t.URI]; exists {
			continue
		}
		seen[t.URI] = struct{}{}
		uris = append(uris, t.URI)
		if len(uris) == limit {
			break
		}
	}
	return uris
}

func (c *Client) doJSON(ctx context.Context, accessToken, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewBuffer(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("spotify api request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w (endpoint=%s)", ErrUnauthorized, endpoint)
	}
	if resp.StatusCode == http.StatusForbidden {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%w (endpoint=%s body=%s)", ErrForbidden, endpoint, string(bodyBytes))
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		return fmt.Errorf("spotify rate limit reached, retry-after=%s", retryAfter)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("spotify api returned %d: %s", resp.StatusCode, string(bodyBytes))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func joinCSV(values []string) string {
	if len(values) == 0 {
		return ""
	}
	out := values[0]
	for i := 1; i < len(values); i++ {
		out += "," + values[i]
	}
	return out
}
