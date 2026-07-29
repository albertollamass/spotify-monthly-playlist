package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"spotify-monthly-playlist/internal/config"
)

const spotifyAuthURL = "https://accounts.spotify.com/authorize"
const spotifyTokenURL = "https://accounts.spotify.com/api/token"

type stateToken struct {
	Value     string
	ExpiresAt time.Time
}

type SpotifyOAuthService struct {
	oauthConfig oauth2.Config
	states      map[string]stateToken
	mu          sync.Mutex
}

func NewSpotifyOAuthService(cfg config.SpotifyConfig) *SpotifyOAuthService {
	log.Printf("spotify oauth scopes configured: %v", cfg.Scopes)
	return &SpotifyOAuthService{
		oauthConfig: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURI,
			Scopes:       cfg.Scopes,
			Endpoint: oauth2.Endpoint{
				AuthURL:  spotifyAuthURL,
				TokenURL: spotifyTokenURL,
			},
		},
		states: make(map[string]stateToken),
	}
}

func (s *SpotifyOAuthService) AuthCodeURL() (string, error) {
	state, err := randomState()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.states[state] = stateToken{Value: state, ExpiresAt: time.Now().Add(10 * time.Minute)}
	s.mu.Unlock()
	return s.oauthConfig.AuthCodeURL(state, oauth2.SetAuthURLParam("show_dialog", "true")), nil
}

func (s *SpotifyOAuthService) Exchange(ctx context.Context, code, state string) (*oauth2.Token, error) {
	if err := s.validateState(state); err != nil {
		return nil, err
	}
	tok, err := s.oauthConfig.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange authorization code: %w", err)
	}
	grantedScope, _ := tok.Extra("scope").(string)
	log.Printf("spotify token granted — scopes: [%s]", grantedScope)
	return tok, nil
}

func (s *SpotifyOAuthService) Refresh(ctx context.Context, refreshToken string) (*oauth2.Token, error) {
	t := &oauth2.Token{RefreshToken: refreshToken}
	token, err := s.oauthConfig.TokenSource(ctx, t).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh spotify token: %w", err)
	}
	if token.RefreshToken == "" {
		token.RefreshToken = refreshToken
	}
	return token, nil
}

func (s *SpotifyOAuthService) validateState(state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.states[state]
	if !ok {
		return errors.New("invalid oauth state")
	}
	delete(s.states, state)
	if time.Now().After(item.ExpiresAt) {
		return errors.New("expired oauth state")
	}
	return nil
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
