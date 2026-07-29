package main

import (
	"bufio"
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"spotify-monthly-playlist/internal/auth"
	"spotify-monthly-playlist/internal/config"
	"spotify-monthly-playlist/internal/db"
	"spotify-monthly-playlist/internal/handlers"
	"spotify-monthly-playlist/internal/httpserver"
	"spotify-monthly-playlist/internal/scheduler"
	"spotify-monthly-playlist/internal/security"
	"spotify-monthly-playlist/internal/spotify"
)

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}

func main() {
	loadDotEnv(".env")
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.Database.URL, cfg.Database.MaxConns)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}
	defer pool.Close()

	if err := db.RunMigrations(ctx, pool, cfg.MigrationsDir); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	repo := db.NewRepository(pool)
	sessionStore := security.NewSessionStore(cfg.Security)
	tokenCipher, err := security.NewTokenCipher(cfg.Security.TokenEncryptionKeyBase64)
	if err != nil {
		log.Fatalf("init token cipher: %v", err)
	}
	oauthService := auth.NewSpotifyOAuthService(cfg.Spotify)
	spotifyClient := spotify.NewClient()
	h := handlers.New(repo, sessionStore, oauthService, spotifyClient, tokenCipher, cfg)

	autoSync := scheduler.NewAutoSync(repo, oauthService, spotifyClient, tokenCipher, cfg.SyncInterval)
	autoSync.Start(ctx)
	defer autoSync.Stop()

	router := httpserver.NewRouter(h, cfg.Security)
	srv := &http.Server{
		Addr:              cfg.ServerAddress,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			log.Printf("server listening on %s (TLS)", cfg.ServerAddress)
			if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && err != http.ErrServerClosed {
				log.Fatalf("listen and serve tls: %v", err)
			}
		} else {
			log.Printf("server listening on %s", cfg.ServerAddress)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("listen and serve: %v", err)
			}
		}
	}()

	if cfg.CallbackAddress != "" {
		callbackSrv := &http.Server{
			Addr:              cfg.CallbackAddress,
			Handler:           httpserver.NewCallbackRouter(h),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      20 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() {
			log.Printf("oauth callback server listening on %s (HTTP)", cfg.CallbackAddress)
			if err := callbackSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("listen and serve callback: %v", err)
			}
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
}
