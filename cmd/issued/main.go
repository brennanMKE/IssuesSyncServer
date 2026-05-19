package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"sync.sstools.co/internal/api"
	"sync.sstools.co/internal/auth"
	"sync.sstools.co/internal/db"
	"sync.sstools.co/internal/storage"
	"sync.sstools.co/internal/ws"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	// Required
	BaseURL          string
	RPID             string
	RPDisplayName    string
	AdminEmail       string
	DatabaseURL      string
	S3Bucket         string
	S3Region         string
	JWTSigningKey    []byte
	InviteSigningKey []byte
	SMTPHost         string
	SMTPPort         string
	SMTPUser         string
	SMTPPass         string
	SMTPFrom         string

	// Optional
	S3Endpoint        string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3PathStyle       bool
}

func loadConfig() (Config, error) {
	var cfg Config
	var missing []string

	required := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	cfg.BaseURL = required("BASE_URL")
	cfg.RPID = required("RP_ID")
	cfg.RPDisplayName = required("RP_DISPLAY_NAME")
	cfg.AdminEmail = required("ADMIN_EMAIL")
	cfg.DatabaseURL = required("DATABASE_URL")
	cfg.S3Bucket = required("S3_BUCKET")
	cfg.S3Region = required("S3_REGION")
	cfg.SMTPHost = required("SMTP_HOST")
	cfg.SMTPPort = required("SMTP_PORT")
	cfg.SMTPUser = required("SMTP_USER")
	cfg.SMTPPass = required("SMTP_PASS")
	cfg.SMTPFrom = required("SMTP_FROM")

	jwtKeyStr := required("JWT_SIGNING_KEY")
	inviteKeyStr := required("INVITE_SIGNING_KEY")

	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required environment variables: %v", missing)
	}

	// Validate SMTP_PORT is numeric.
	if _, err := strconv.Atoi(cfg.SMTPPort); err != nil {
		return cfg, fmt.Errorf("SMTP_PORT must be an integer: %w", err)
	}

	cfg.JWTSigningKey = []byte(jwtKeyStr)
	cfg.InviteSigningKey = []byte(inviteKeyStr)

	// Optional vars
	cfg.S3Endpoint = os.Getenv("S3_ENDPOINT")
	cfg.S3AccessKeyID = os.Getenv("S3_ACCESS_KEY_ID")
	cfg.S3SecretAccessKey = os.Getenv("S3_SECRET_ACCESS_KEY")
	cfg.S3PathStyle = os.Getenv("S3_PATH_STYLE") == "true"

	return cfg, nil
}

func main() {
	// Structured JSON logger to stdout.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("issued starting", "sha", BuildSHA)

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to Postgres.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to create database pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("database ping failed", "err", err)
		os.Exit(1)
	}
	slog.Info("database connected")

	// Run migrations.
	if err := db.Migrate(ctx, pool); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
	slog.Info("migrations complete")

	// Bootstrap first admin user.
	if err := auth.Bootstrap(ctx, pool, cfg.AdminEmail, cfg.InviteSigningKey, cfg.BaseURL); err != nil {
		slog.Error("bootstrap failed", "err", err)
		os.Exit(1)
	}

	// Build auth service.
	authSvc, err := auth.NewService(auth.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPDisplayName,
		RPOrigins:     []string{cfg.BaseURL},
		JWTKey:        cfg.JWTSigningKey,
		InviteKey:     cfg.InviteSigningKey,
	}, pool)
	if err != nil {
		slog.Error("failed to create auth service", "err", err)
		os.Exit(1)
	}

	// Build S3 client.
	s3Cfg := storage.S3Config{
		Bucket:          cfg.S3Bucket,
		Region:          cfg.S3Region,
		Endpoint:        cfg.S3Endpoint,
		AccessKeyID:     cfg.S3AccessKeyID,
		SecretAccessKey: cfg.S3SecretAccessKey,
		PathStyle:       cfg.S3PathStyle,
	}
	s3Client, err := storage.NewS3Client(ctx, s3Cfg)
	if err != nil {
		slog.Error("failed to create S3 client", "err", err)
		os.Exit(1)
	}
	slog.Info("S3 client ready", "bucket", cfg.S3Bucket)

	// Build in-memory LRU cache.
	cache := storage.NewLRUCache(64 * 1024 * 1024) // 64 MB

	// Build WebSocket hub.
	hub := ws.NewHub()
	go hub.Run()

	// Wire up HTTP router.
	deps := api.Deps{
		BuildSHA: BuildSHA,
		Auth:     authSvc,
		DB:       pool,
		Pool:     pool,
		S3Client: s3Client,
		Cache:    cache,
		Hub:      hub,
	}
	handler := api.NewRouter(deps)

	srv := &http.Server{
		Addr:         "127.0.0.1:8080",
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start server in background.
	go func() {
		slog.Info("HTTP server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Wait for signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	slog.Info("shutdown signal received", "signal", sig)

	// Graceful shutdown with 30s timeout.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	} else {
		slog.Info("server stopped cleanly")
	}
}
