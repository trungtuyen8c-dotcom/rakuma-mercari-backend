package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/config"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/db"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/routers"
	"github.com/trungtuyen8c-dotcom/rakuma-mercari-backend/internal/service"
)

func main() {
	cfg := config.Load()
	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("connect database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	if err := service.EnsureOpenPeriod(ctx, pool, time.Now()); err != nil {
		slog.Error("bootstrap period", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: routers.New(service.New(pool, cfg)), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		slog.Info("listening", "addr", srv.Addr, "env", cfg.AppEnv, "google", cfg.GoogleEnabled(), "devLogin", cfg.DevLoginEnabled())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}
