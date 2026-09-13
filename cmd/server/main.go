package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"deepseek-moderation-api/internal/audit"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	vault, err := audit.NewVault(os.Getenv("MASTER_KEY"))
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	startup, done := context.WithTimeout(ctx, 30*time.Second)
	defer done()
	store, err := audit.OpenStore(startup, os.Getenv("DATABASE_URL"), vault)
	if err != nil {
		return err
	}
	defer store.DB.Close()
	if err = store.Bootstrap(startup, env("ADMIN_USER", "admin"), os.Getenv("ADMIN_PASSWORD")); err != nil {
		return err
	}
	limits, err := audit.RuntimeConfigFromEnv()
	if err != nil {
		return err
	}
	app, err := audit.NewServer(store, env("PUBLIC_URL", "http://localhost:8090"), env("STATIC_DIR", "frontend/dist"), limits)
	if err != nil {
		return err
	}
	defer app.Close()
	if err := store.RecoverEvaluations(startup); err != nil {
		return err
	}
	server := &http.Server{Addr: env("LISTEN_ADDR", "127.0.0.1:8090"), Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go app.CleanupLoop(ctx)
	stopped := make(chan error, 1)
	go func() {
		slog.Info("audit service listening", "address", server.Addr)
		stopped <- server.ListenAndServe()
	}()
	select {
	case err = <-stopped:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		stop, finish := context.WithTimeout(context.Background(), 10*time.Second)
		defer finish()
		return server.Shutdown(stop)
	}
}
