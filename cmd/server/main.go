package main

import (
	"context"
	"errors"
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
	"github.com/HeMMars4/simple-finance/internal/bot"
	"github.com/HeMMars4/simple-finance/internal/handlers"
	"github.com/HeMMars4/simple-finance/internal/storage"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := config.Load()

	db, err := storage.New(cfg.DB, cfg.EncryptionKey)
	if err != nil {
		slog.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := goose.SetDialect("postgres"); err != nil {
		slog.Error("goose dialect", "err", err)
		os.Exit(1)
	}
	if err := goose.Up(db.DB.DB, "migrations"); err != nil && !errors.Is(err, goose.ErrNoNextVersion) {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}

	authSvc := auth.New(cfg.Secret)
	agg := handlers.NewAggregator(cfg, db)
	botRunner := bot.New(db)
	h := handlers.New(agg, authSvc, db, botRunner, "web/templates")

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		users, err := db.ListUsers(ctx)
		if err != nil || len(users) == 0 {
			slog.Info("no users yet, skipping initial refresh")
			return
		}
		for _, u := range users {
			if err := agg.RefreshAll(ctx, u.ID); err != nil {
				slog.Warn("initial refresh partial", "user", u.Username, "err", err)
			} else {
				slog.Info("initial data loaded", "user", u.Username)
			}
		}
	}()

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	r.With(middleware.Timeout(10 * time.Second)).Get("/", h.Landing)

	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(30 * time.Second))
		r.Get("/login", h.LoginPage)
		r.Post("/login", h.LoginSubmit)
		r.Get("/logout", h.Logout)
		r.Get("/register", h.RegisterPage)
		r.Post("/register", h.RegisterSubmit)
	})

	r.Group(func(r chi.Router) {
		r.Use(authSvc.Middleware)
		r.Use(h.InjectUserID)

		// Dashboard
		r.With(middleware.Timeout(30 * time.Second)).Get("/dashboard", h.Dashboard)
		r.With(middleware.Timeout(3 * time.Minute)).Post("/refresh", h.Refresh)

		// Settings — integration keys
		r.With(middleware.Timeout(30 * time.Second)).Get("/settings", h.Settings)
		r.With(middleware.Timeout(30 * time.Second)).Post("/settings", h.SaveSettings)

		// Settings — manual assets
		r.With(middleware.Timeout(15 * time.Second)).Post("/settings/assets/manual/create", h.CreateManualAsset)
		r.With(middleware.Timeout(15 * time.Second)).Post("/settings/assets/manual/update", h.UpdateManualAsset)
		r.With(middleware.Timeout(15 * time.Second)).Post("/settings/assets/manual/delete", h.DeleteManualAsset)

		// Settings — asset visibility
		r.With(middleware.Timeout(15 * time.Second)).Post("/settings/assets/toggle", h.ToggleAssetVisibility)

		// Settings — extra integration instances
		r.With(middleware.Timeout(15 * time.Second)).Post("/settings/integrations/create", h.CreateIntegrationInstance)
		r.With(middleware.Timeout(15 * time.Second)).Post("/settings/integrations/delete", h.DeleteIntegrationInstance)

		// Settings — change password
		r.With(middleware.Timeout(30 * time.Second)).Post("/settings/user/password", h.ChangePassword)

		// Development / AI trading
		r.With(middleware.Timeout(30 * time.Second)).Get("/develop", h.Development)
		r.With(middleware.Timeout(30 * time.Second)).Post("/develop", h.SaveDevelopSettings)
		r.With(middleware.Timeout(2 * time.Minute)).Post("/develop/analyze", h.AnalyzePortfolio)
		r.With(middleware.Timeout(30 * time.Second)).Post("/develop/trade", h.ExecuteTrade)

		// Bot daemon control — trader (original)
		r.With(middleware.Timeout(10 * time.Second)).Get("/develop/bot/status", h.BotStatus)
		r.With(middleware.Timeout(10 * time.Second)).Post("/develop/bot/start", h.BotStart)
		r.With(middleware.Timeout(10 * time.Second)).Post("/develop/bot/stop", h.BotStop)
		r.With(middleware.Timeout(10 * time.Second)).Get("/develop/bot/logs", h.BotLogs)
		r.With(middleware.Timeout(10 * time.Second)).Post("/develop/bot/logs/clear", h.BotLogsClear)
		// Bot daemon control — investor
		r.With(middleware.Timeout(10 * time.Second)).Get("/develop/bot/investor/status", h.InvestorBotStatus)
		r.With(middleware.Timeout(10 * time.Second)).Post("/develop/bot/investor/start", h.InvestorBotStart)
		r.With(middleware.Timeout(10 * time.Second)).Post("/develop/bot/investor/stop", h.InvestorBotStop)
		// Bot daemon control — bybit
		r.With(middleware.Timeout(10 * time.Second)).Get("/develop/bot/bybit/status", h.BybitBotStatus)
		r.With(middleware.Timeout(10 * time.Second)).Post("/develop/bot/bybit/start", h.BybitBotStart)
		r.With(middleware.Timeout(10 * time.Second)).Post("/develop/bot/bybit/stop", h.BybitBotStop)


	})

	addr := fmt.Sprintf(":%s", cfg.Port)
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		slog.Info("starting Simple Finance (HTTPS)", "addr", addr)
		if err := http.ListenAndServeTLS(addr, cfg.TLSCert, cfg.TLSKey, r); err != nil {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	} else {
		slog.Info("starting Simple Finance (HTTP)", "addr", addr)
		if err := http.ListenAndServe(addr, r); err != nil {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}
}
