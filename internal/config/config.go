package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"spotify-monthly-playlist/internal/security"
)

type Config struct {
	ServerAddress   string
	CallbackAddress string
	AppURL          string
	MigrationsDir   string
	TLSCertFile     string
	TLSKeyFile      string
	Database        DatabaseConfig
	Spotify         SpotifyConfig
	Security        security.Config
	SyncInterval    time.Duration
}

type DatabaseConfig struct {
	URL      string
	MaxConns int32
}

type SpotifyConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Scopes       []string
}

func Load() (Config, error) {
	cfg := Config{
		ServerAddress:   getEnv("APP_ADDRESS", ":8080"),
		CallbackAddress: os.Getenv("CALLBACK_ADDRESS"),
		AppURL:          os.Getenv("APP_URL"),
		MigrationsDir:   getEnv("MIGRATIONS_DIR", "migrations"),
		TLSCertFile:     os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:      os.Getenv("TLS_KEY_FILE"),
		Database: DatabaseConfig{
			URL:      os.Getenv("DATABASE_URL"),
			MaxConns: int32(getEnvInt("DATABASE_MAX_CONNS", 10)),
		},
		Spotify: SpotifyConfig{
			ClientID:     os.Getenv("SPOTIFY_CLIENT_ID"),
			ClientSecret: os.Getenv("SPOTIFY_CLIENT_SECRET"),
			RedirectURI:  os.Getenv("SPOTIFY_REDIRECT_URI"),
			Scopes:       splitCSV(getEnv("SPOTIFY_SCOPES", "user-read-recently-played,user-top-read,playlist-modify-private")),
		},
		Security: security.Config{
			SessionCookieName:        getEnv("SESSION_COOKIE_NAME", "spotify_monthly_session"),
			SessionTTL:               time.Duration(getEnvInt("SESSION_TTL_MINUTES", 120)) * time.Minute,
			CookieSecure:             getEnvBool("COOKIE_SECURE", false),
			CookieDomain:             os.Getenv("COOKIE_DOMAIN"),
			TrustedProxyCIDRs:        splitCSV(os.Getenv("TRUSTED_PROXY_CIDRS")),
			SessionSecret:            os.Getenv("SESSION_SECRET"),
			TokenEncryptionKeyBase64: os.Getenv("TOKEN_ENCRYPTION_KEY_BASE64"),
			RateLimitPerMin:          getEnvInt("RATE_LIMIT_PER_MINUTE", 60),
		},
		SyncInterval: time.Duration(getEnvInt("SYNC_INTERVAL_MINUTES", 60)) * time.Minute,
	}

	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validate(cfg Config) error {
	var missing []string
	if cfg.Database.URL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if cfg.Spotify.ClientID == "" {
		missing = append(missing, "SPOTIFY_CLIENT_ID")
	}
	if cfg.Spotify.ClientSecret == "" {
		missing = append(missing, "SPOTIFY_CLIENT_SECRET")
	}
	if cfg.Spotify.RedirectURI == "" {
		missing = append(missing, "SPOTIFY_REDIRECT_URI")
	}
	if cfg.Security.SessionSecret == "" {
		missing = append(missing, "SESSION_SECRET")
	}
	if cfg.Security.TokenEncryptionKeyBase64 == "" {
		missing = append(missing, "TOKEN_ENCRYPTION_KEY_BASE64")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if len(cfg.Security.SessionSecret) < 32 {
		return errors.New("SESSION_SECRET must be at least 32 characters")
	}
	return nil
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	val := getEnv(key, "")
	if val == "" {
		return fallback
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	val := strings.ToLower(strings.TrimSpace(getEnv(key, "")))
	if val == "" {
		return fallback
	}
	return val == "1" || val == "true" || val == "yes"
}

func splitCSV(val string) []string {
	if strings.TrimSpace(val) == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
