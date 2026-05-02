package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"rago/internal/handler"
	"rago/internal/lmstudio"
	"rago/internal/postgres"
	"rago/internal/service"
)

func init() {
	_ = godotenv.Load()
}

func main() {
	initLogger()

	// Infrastructure
	db, err := postgres.Connect()
	if err != nil {
		slog.Error("db connect failed", "error", err)
		os.Exit(1)
	}
	if err := postgres.Migrate(db); err != nil {
		slog.Error("db migrate failed", "error", err)
		os.Exit(1)
	}

	lmURL := os.Getenv("LM_STUDIO_URL")
	if lmURL == "" {
		lmURL = "http://localhost:1234"
	}
	embedder := lmstudio.NewEmbedder(lmURL, os.Getenv("LM_STUDIO_MODEL"))
	chatClient := lmstudio.NewChatClient(lmURL, os.Getenv("LM_STUDIO_CHAT_MODEL"))

	// Domain
	repo := postgres.NewRepository(db)
	svc := service.NewRAGService(repo, embedder, chatClient)

	// Transport
	mux := http.NewServeMux()
	handler.New(svc).Register(mux)

	srv := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	// Graceful shutdown on SIGINT / SIGTERM (Ctrl+C)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	slog.Info("server stopped")
}

func initLogger() {
	var level slog.Level
	switch strings.ToUpper(os.Getenv("LOG_LEVEL")) {
	case "DEBUG":
		level = slog.LevelDebug
	case "INFO":
		level = slog.LevelInfo
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelWarn
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})))
}
