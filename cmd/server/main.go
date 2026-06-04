package main

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/pressly/goose/v3"

	"github.com/HeMMars4/simple-finance/config"
	"github.com/HeMMars4/simple-finance/internal/auth"
	"github.com/HeMMars4/simple-finance/internal/handlers"
	"github.com/HeMMars4/simple-finance/internal/storage"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := config.Load()

	// Database
	db, err := storage.New(cfg.DB)
	if err != nil {
		slog.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	// Run migrations
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		slog.Error("goose dialect", "err", err)
		os.Exit(1)
	}
	if err := goose.Up(db.DB.DB, "migrations"); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}

	// Services
	authSvc := auth.New(cfg.Secret, cfg.AdminUsername, cfg.AdminPassword)
	agg := handlers.NewAggregator(cfg, db)
	h := handlers.New(agg, authSvc, db, "web/templates")

	// Initial data fetch in background
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := agg.RefreshAll(ctx); err != nil {
			slog.Warn("initial refresh partial", "err", err)
		} else {
			slog.Info("initial data loaded")
		}
	}()

	// Router
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Static files
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	// Public routes
	r.Get("/login", h.LoginPage)
	r.Post("/login", h.LoginSubmit)
	r.Get("/logout", h.Logout)

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(authSvc.Middleware)
		r.Get("/", h.Dashboard)
		r.Post("/refresh", h.Refresh)
	})

	addr := fmt.Sprintf(":%s", cfg.Port)
	slog.Info("starting Simple Finance", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
