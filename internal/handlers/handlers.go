package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/HeMMars4/simple-finance/internal/auth"
	"github.com/HeMMars4/simple-finance/internal/bot"
	"github.com/HeMMars4/simple-finance/internal/integrations/claude"
	"github.com/HeMMars4/simple-finance/internal/integrations/tinvest"
	"github.com/HeMMars4/simple-finance/internal/models"
	"github.com/HeMMars4/simple-finance/internal/storage"
)

var moscow, _ = time.LoadLocation("Europe/Moscow")

type ctxKeyUserID struct{}

// InjectUserID resolves the authenticated username to a DB user ID and
// stores it in the request context. Must run after auth.Middleware.
func (h *Handler) InjectUserID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username := auth.GetUsername(r)
		user, err := h.db.GetUser(r.Context(), username)
		if err != nil || user == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyUserID{}, user.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func ctxUserID(r *http.Request) int64 {
	id, _ := r.Context().Value(ctxKeyUserID{}).(int64)
	return id
}

type Handler struct {
	agg     *Aggregator
	auth    *auth.Auth
	db      *storage.DB
	bot     *bot.Runner
	tmplDir string
}

func New(agg *Aggregator, a *auth.Auth, db *storage.DB, botRunner *bot.Runner, templateDir string) *Handler {
	return &Handler{agg: agg, auth: a, db: db, bot: botRunner, tmplDir: templateDir}
}

func (h *Handler) render(w http.ResponseWriter, name string, data any, files ...string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmplFiles := append([]string{filepath.Join(h.tmplDir, "base.html")}, files...)
	tmpl := template.Must(template.New(name).Funcs(templateFuncs()).ParseFiles(tmplFiles...))
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) renderLogin(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	h.render(w, name, data, filepath.Join(h.tmplDir, name))
}

func (h *Handler) Landing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeFile(w, r, filepath.Join(h.tmplDir, "landing.html"))
}

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("sf_token")
	if err == nil && h.auth.ValidateToken(cookie.Value) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	h.renderLogin(w, "login.html", nil)
}

func (h *Handler) RegisterPage(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("sf_token"); err == nil && h.auth.ValidateToken(cookie.Value) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	h.renderLogin(w, "register.html", nil)
}

func (h *Handler) RegisterSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	password2 := r.FormValue("password2")

	renderErr := func(msg string) {
		h.renderLogin(w, "register.html", struct{ Error string }{Error: msg})
	}

	if username == "" || password == "" {
		renderErr("Имя пользователя и пароль обязательны")
		return
	}
	if len(password) < 6 {
		renderErr("Пароль должен быть не короче 6 символов")
		return
	}
	if password != password2 {
		renderErr("Пароли не совпадают")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		renderErr("Внутренняя ошибка")
		return
	}
	if err := h.db.CreateUser(r.Context(), username, string(hash)); err != nil {
		renderErr("Ошибка создания пользователя: " + err.Error())
		return
	}

	slog.Info("first user created", "username", username)
	token, err := h.auth.GenerateToken(username)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "sf_token", Value: token, Path: "/", MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (h *Handler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	username := r.FormValue("username")
	password := r.FormValue("password")

	fail := func() {
		slog.Warn("login failed", "user", username)
		h.renderLogin(w, "login.html", struct{ Error string }{Error: "Неверный логин или пароль"})
	}

	user, err := h.db.GetUser(r.Context(), username)
	if err != nil {
		fail()
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		fail()
		return
	}

	token, err := h.auth.GenerateToken(username)
	if err != nil {
		http.Error(w, "token error", http.StatusInternalServerError)
		return
	}
	slog.Info("login success", "user", username)
	http.SetCookie(w, &http.Cookie{
		Name: "sf_token", Value: token, Path: "/", MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "sf_token", Value: "", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	slog.Info("Dashboard called", "path", r.URL.Path)
	data, err := h.agg.BuildDashboard(r.Context(), ctxUserID(r))
	if data == nil {
		data = &models.DashboardData{Rates: map[string]float64{}}
	}
	if err != nil {
		data.Error = err.Error()
	}
	h.render(w, "dashboard.html", data, filepath.Join(h.tmplDir, "dashboard.html"), filepath.Join(h.tmplDir, "dashboard_content.html"))
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	err := h.agg.RefreshAll(r.Context(), uid)
	data, dbErr := h.agg.BuildDashboard(r.Context(), uid)
	if data == nil {
		data = &models.DashboardData{Rates: map[string]float64{}}
	}
	if err != nil {
		data.Error = err.Error()
	} else if dbErr != nil {
		data.Error = "Ошибка чтения данных из БД"
	}
	h.render(w, "dashboard_content.html", data, filepath.Join(h.tmplDir, "dashboard_content.html"))
}

// SettingsData holds data for the settings page
type SettingsData struct {
	KeysSet        map[string]bool // true when a key is saved; real values are never sent to the browser
	CurrentUser    string
	Success        bool
	Error          string
	AllAssets      []models.Asset
	HiddenAssets   map[string]bool
	ManualAssets   []models.ManualAsset
	ExtraInstances []models.IntegrationInstance
}

func (h *Handler) loadSettingsData(r *http.Request) *SettingsData {
	ctx := r.Context()
	uid := ctxUserID(r)
	keys, _ := h.db.GetAllAPIKeys(ctx, uid)
	keysSet := make(map[string]bool, len(keys))
	for k, v := range keys {
		keysSet[k] = v != ""
	}
	assets, _ := h.db.GetAllAssets(ctx, uid)
	hidden, _ := h.db.GetHiddenAssets(ctx, uid)
	manuals, _ := h.db.GetManualAssets(ctx, uid)
	instances, _ := h.db.GetIntegrationInstances(ctx, uid)
	return &SettingsData{
		KeysSet:        keysSet,
		CurrentUser:    auth.GetUsername(r),
		AllAssets:      assets,
		HiddenAssets:   hidden,
		ManualAssets:   manuals,
		ExtraInstances: instances,
	}
}

func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	h.render(w, "settings.html", h.loadSettingsData(r), filepath.Join(h.tmplDir, "settings.html"))
}

func (h *Handler) SaveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	fields := []string{
		"tbank_session_id", "tinvest_token",
		"bybit_api_key", "bybit_api_secret",
		"btc_addresses",
		"monero_rpc_url", "monero_address", "monero_view_key", "monero_restore_height",
		"steam_id", "steam_app_id",
	}

	ctx := r.Context()
	uid := ctxUserID(r)
	for _, field := range fields {
		val := strings.TrimSpace(r.FormValue(field))
		if val == "" {
			continue // empty = keep existing key unchanged
		}
		if err := h.db.SetAPIKey(ctx, uid, field, val); err != nil {
			slog.Error("save api key", "key", field, "err", err)
			d := h.loadSettingsData(r)
			d.Error = "Ошибка сохранения: " + err.Error()
			h.render(w, "settings.html", d, filepath.Join(h.tmplDir, "settings.html"))
			return
		}
	}

	slog.Info("api keys saved")
	d := h.loadSettingsData(r)
	d.Success = true
	h.render(w, "settings.html", d, filepath.Join(h.tmplDir, "settings.html"))
}

// --- Manual assets ---

func parseManualAssetType(s string) models.AssetType {
	if s == "debt" {
		return models.AssetTypeCreditCard
	}
	return models.AssetTypeCash
}

func (h *Handler) CreateManualAsset(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	uid := ctxUserID(r)
	name := strings.TrimSpace(r.FormValue("asset_name"))
	currency := strings.ToUpper(strings.TrimSpace(r.FormValue("asset_currency")))
	amount, _ := strconv.ParseFloat(r.FormValue("asset_amount"), 64)
	assetType := parseManualAssetType(r.FormValue("asset_type"))

	if name == "" || amount <= 0 || currency == "" {
		d := h.loadSettingsData(r)
		d.Error = "Заполните все поля: название, сумма и валюта"
		h.render(w, "settings.html", d, filepath.Join(h.tmplDir, "settings.html"))
		return
	}

	if err := h.db.CreateManualAsset(r.Context(), uid, name, assetType, amount, currency); err != nil {
		d := h.loadSettingsData(r)
		d.Error = "Ошибка сохранения: " + err.Error()
		h.render(w, "settings.html", d, filepath.Join(h.tmplDir, "settings.html"))
		return
	}
	http.Redirect(w, r, "/settings", http.StatusFound)
}

func (h *Handler) UpdateManualAsset(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	uid := ctxUserID(r)
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	name := strings.TrimSpace(r.FormValue("asset_name"))
	currency := strings.ToUpper(strings.TrimSpace(r.FormValue("asset_currency")))
	amount, _ := strconv.ParseFloat(r.FormValue("asset_amount"), 64)
	assetType := parseManualAssetType(r.FormValue("asset_type"))

	if name == "" || amount <= 0 || currency == "" {
		d := h.loadSettingsData(r)
		d.Error = "Заполните все поля: название, сумма и валюта"
		h.render(w, "settings.html", d, filepath.Join(h.tmplDir, "settings.html"))
		return
	}

	if err := h.db.UpdateManualAsset(r.Context(), uid, id, name, assetType, amount, currency); err != nil {
		d := h.loadSettingsData(r)
		d.Error = "Ошибка обновления: " + err.Error()
		h.render(w, "settings.html", d, filepath.Join(h.tmplDir, "settings.html"))
		return
	}
	http.Redirect(w, r, "/settings", http.StatusFound)
}

func (h *Handler) DeleteManualAsset(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	uid := ctxUserID(r)
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	_ = h.db.DeleteManualAsset(r.Context(), uid, id)
	http.Redirect(w, r, "/settings", http.StatusFound)
}

// --- Asset visibility ---

func (h *Handler) ToggleAssetVisibility(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	uid := ctxUserID(r)
	assetKey := r.FormValue("asset_key")
	hidden := r.FormValue("hidden") == "true"
	_ = h.db.SetAssetHidden(r.Context(), uid, assetKey, hidden)
	http.Redirect(w, r, "/settings", http.StatusFound)
}

// --- Extra integration instances ---

func (h *Handler) CreateIntegrationInstance(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	uid := ctxUserID(r)
	instType := r.FormValue("inst_type")
	label := strings.TrimSpace(r.FormValue("inst_label"))
	if label == "" {
		label = instType
	}

	var config map[string]any
	switch instType {
	case "steam":
		config = map[string]any{
			"steam_id": strings.TrimSpace(r.FormValue("inst_steam_id")),
			"app_id":   strings.TrimSpace(r.FormValue("inst_app_id")),
		}
	case "monero":
		config = map[string]any{
			"address":        strings.TrimSpace(r.FormValue("inst_monero_address")),
			"view_key":       strings.TrimSpace(r.FormValue("inst_monero_view_key")),
			"restore_height": strings.TrimSpace(r.FormValue("inst_monero_restore_height")),
			"rpc_url":        strings.TrimSpace(r.FormValue("inst_monero_rpc_url")),
		}
	case "btc":
		config = map[string]any{
			"addresses": strings.TrimSpace(r.FormValue("inst_btc_addresses")),
		}
	default:
		http.Redirect(w, r, "/settings", http.StatusFound)
		return
	}

	configJSON, _ := json.Marshal(config)
	if err := h.db.CreateIntegrationInstance(r.Context(), uid, instType, label, string(configJSON)); err != nil {
		d := h.loadSettingsData(r)
		d.Error = "Ошибка создания интеграции: " + err.Error()
		h.render(w, "settings.html", d, filepath.Join(h.tmplDir, "settings.html"))
		return
	}
	http.Redirect(w, r, "/settings", http.StatusFound)
}

func (h *Handler) DeleteIntegrationInstance(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	uid := ctxUserID(r)
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	_ = h.db.DeleteIntegrationInstance(r.Context(), uid, id)
	http.Redirect(w, r, "/settings", http.StatusFound)
}

func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	current := auth.GetUsername(r)
	oldPass := r.FormValue("old_password")
	newPass := r.FormValue("new_password")

	renderErr := func(msg string) {
		d := h.loadSettingsData(r)
		d.Error = msg
		h.render(w, "settings.html", d, filepath.Join(h.tmplDir, "settings.html"))
	}

	if len(newPass) < 6 {
		renderErr("Новый пароль должен быть не короче 6 символов")
		return
	}
	user, err := h.db.GetUser(r.Context(), current)
	if err != nil {
		renderErr("Ошибка получения данных пользователя")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPass)) != nil {
		renderErr("Неверный текущий пароль")
		return
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err := h.db.UpdatePassword(r.Context(), current, string(hash)); err != nil {
		renderErr("Ошибка обновления пароля: " + err.Error())
		return
	}

	slog.Info("password changed", "username", current)
	d := h.loadSettingsData(r)
	d.Success = true
	h.render(w, "settings.html", d, filepath.Join(h.tmplDir, "settings.html"))
}

// --- Development (AI Trading) ---

type DevelopData struct {
	Settings       *models.UserSettings
	Saved          bool
	Error          string
	HasPortfolio   bool
	TraderRunning  bool
	InvestorRunning bool
	BybitRunning   bool
	InvestTotalRUB float64
	BybitTotalRUB  float64
}

func (h *Handler) Development(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	settings, _ := h.db.GetUserSettings(r.Context(), uid)
	assets, _ := h.db.GetAllAssets(r.Context(), uid)

	hasPortfolio := false
	var investTotal, bybitTotal float64
	for _, a := range assets {
		switch a.Type {
		case models.AssetTypeInvest:
			hasPortfolio = true
			investTotal += a.AmountRUB
		case models.AssetTypeExchange:
			if strings.Contains(a.Source, "bybit") {
				bybitTotal += a.AmountRUB
			}
		}
	}

	d := &DevelopData{
		Settings:        settings,
		Saved:           r.URL.Query().Get("saved") == "1",
		HasPortfolio:    hasPortfolio,
		TraderRunning:   h.bot.IsTraderRunning(uid),
		InvestorRunning: h.bot.IsInvestorRunning(uid),
		BybitRunning:    h.bot.IsBybitRunning(uid),
		InvestTotalRUB:  investTotal,
		BybitTotalRUB:   bybitTotal,
	}
	h.render(w, "development.html", d, filepath.Join(h.tmplDir, "development.html"))
}

func (h *Handler) SaveDevelopSettings(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	uid := ctxUserID(r)

	// Load existing as base — each form only overwrites its own fields
	existing, _ := h.db.GetUserSettings(r.Context(), uid)
	if existing == nil {
		existing = &models.UserSettings{
			UserID:                uid,
			RiskLevel:             "medium",
			MaxLossPct:            5.0,
			BotIntervalMinutes:    60,
			InvestorIntervalHours: 24,
			BybitBotIntervalMin:   60,
		}
	}

	// Copy existing, then patch only the fields that belong to this form
	s := *existing
	s.UserID = uid
	// Always reflect live bot state
	s.BotEnabled = h.bot.IsTraderRunning(uid)
	s.InvestorBotEnabled = h.bot.IsInvestorRunning(uid)
	s.BybitBotEnabled = h.bot.IsBybitRunning(uid)

	switch r.FormValue("__bot__") {

	case "keys":
		if v := strings.TrimSpace(r.FormValue("claude_api_key")); v != "" {
			s.ClaudeAPIKey = v
		}
		if v := strings.TrimSpace(r.FormValue("tinvest_trade_token")); v != "" {
			s.TInvestTradeToken = v
		}
		// Save first, then restart running bots so they pick up the new keys immediately
		if err := h.db.SaveUserSettings(r.Context(), &s); err != nil {
			slog.Error("save develop settings (keys)", "err", err)
		} else {
			traderWasRunning := h.bot.IsTraderRunning(uid)
			investorWasRunning := h.bot.IsInvestorRunning(uid)
			if traderWasRunning {
				h.bot.StopTrader(uid)
			}
			if investorWasRunning {
				h.bot.StopInvestor(uid)
			}
			if traderWasRunning || investorWasRunning {
				go func() {
					time.Sleep(400 * time.Millisecond)
					if traderWasRunning {
						h.bot.StartTrader(uid)
					}
					if investorWasRunning {
						h.bot.StartInvestor(uid)
					}
				}()
			}
		}
		http.Redirect(w, r, "/develop?saved=1", http.StatusFound)
		return

	case "investor":
		if v := r.FormValue("risk_level"); v != "" {
			s.RiskLevel = v
		}
		if v, _ := strconv.ParseFloat(r.FormValue("max_loss_pct"), 64); v > 0 {
			s.MaxLossPct = v
		}
		if v, _ := strconv.Atoi(r.FormValue("investor_interval_hours")); v >= 1 {
			s.InvestorIntervalHours = v
		}

	case "trader":
		if v := r.FormValue("risk_level"); v != "" {
			s.RiskLevel = v
		}
		if v, _ := strconv.ParseFloat(r.FormValue("max_loss_pct"), 64); v > 0 {
			s.MaxLossPct = v
		}
		if v, _ := strconv.Atoi(r.FormValue("bot_interval_minutes")); v >= 15 {
			s.BotIntervalMinutes = v
		}
		s.BotUseMargin = r.FormValue("bot_use_margin") == "on"

	case "bybit":
		if v := r.FormValue("risk_level"); v != "" {
			s.RiskLevel = v
		}
		if v, _ := strconv.ParseFloat(r.FormValue("max_loss_pct"), 64); v > 0 {
			s.MaxLossPct = v
		}
		if v, _ := strconv.Atoi(r.FormValue("bybit_bot_interval_min")); v >= 15 {
			s.BybitBotIntervalMin = v
		}
	}

	if err := h.db.SaveUserSettings(r.Context(), &s); err != nil {
		slog.Error("save develop settings", "err", err)
	}
	http.Redirect(w, r, "/develop?saved=1", http.StatusFound)
}

// --- Bot control handlers ---

func botStatusFragment(running bool) string {
	if running {
		return `<div id="bot-control" hx-get="/develop/bot/status" hx-trigger="every 10s" hx-swap="outerHTML" style="display:flex;align-items:center;gap:16px;flex-wrap:wrap">
  <div style="display:flex;align-items:center;gap:8px">
    <span style="color:var(--green);font-size:16px;animation:pulse 2s infinite">●</span>
    <span style="font-size:12px;color:var(--green);font-weight:600">Бот работает</span>
  </div>
  <form hx-post="/develop/bot/stop" hx-target="#bot-control" hx-swap="outerHTML">
    <button type="submit" class="btn danger" style="font-size:10px;padding:6px 12px">Остановить</button>
  </form>
</div>`
	}
	return `<div id="bot-control" hx-get="/develop/bot/status" hx-trigger="every 10s" hx-swap="outerHTML" style="display:flex;align-items:center;gap:16px;flex-wrap:wrap">
  <div style="display:flex;align-items:center;gap:8px">
    <span style="color:var(--muted);font-size:16px">○</span>
    <span style="font-size:12px;color:var(--muted)">Бот остановлен</span>
  </div>
  <form hx-post="/develop/bot/start" hx-target="#bot-control" hx-swap="outerHTML">
    <button type="submit" class="btn primary" style="font-size:10px;padding:6px 12px">Запустить бот</button>
  </form>
</div>`
}

func (h *Handler) BotStatus(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, botStatusFragment(h.bot.IsRunning(uid)))
}

func (h *Handler) BotStart(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	h.bot.Start(uid)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, botStatusFragment(true))
}

func (h *Handler) BotStop(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	h.bot.Stop(uid)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, botStatusFragment(false))
}

func (h *Handler) BotLogs(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	logs, _ := h.db.GetBotLogs(r.Context(), uid, 60)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(logs) == 0 {
		fmt.Fprint(w, `<div style="color:var(--muted);font-size:11px;padding:8px 0">Журнал пуст</div>`)
		return
	}
	for _, l := range logs {
		color := "var(--subtext)"
		switch l.Level {
		case "error":
			color = "var(--red)"
		case "warn":
			color = "var(--gold)"
		case "trade":
			color = "var(--green)"
		}
		ts := l.CreatedAt.UTC()
		if moscow != nil {
			ts = l.CreatedAt.In(moscow)
		}
		fmt.Fprintf(w, `<div style="display:flex;gap:10px;padding:3px 0;border-bottom:1px solid rgba(30,35,41,.4);font-size:11px;font-family:var(--mono)"><span style="color:var(--muted);white-space:nowrap;flex-shrink:0">%s</span><span style="color:%s">%s</span></div>`,
			ts.Format("02.01 15:04:05"),
			color,
			template.HTMLEscapeString(l.Message),
		)
	}
}

func (h *Handler) BotLogsClear(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	h.db.ClearBotLogs(r.Context(), uid) //nolint:errcheck
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<div style="color:var(--muted);font-size:11px;padding:8px 0">Журнал очищен</div>`)
}

// --- Investor bot ---

func (h *Handler) InvestorBotStatus(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, investorStatusFragment(h.bot.IsInvestorRunning(uid)))
}

func (h *Handler) InvestorBotStart(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	h.bot.StartInvestor(uid)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, investorStatusFragment(true))
}

func (h *Handler) InvestorBotStop(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	h.bot.StopInvestor(uid)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, investorStatusFragment(false))
}

func investorStatusFragment(running bool) string {
	if running {
		return `<div id="investor-control" hx-get="/develop/bot/investor/status" hx-trigger="every 10s" hx-swap="outerHTML" style="display:flex;align-items:center;gap:12px">` +
			`<span style="color:var(--green);font-size:14px;animation:pulse 2s infinite">●</span>` +
			`<span style="font-size:11px;color:var(--green);font-weight:600">Активен</span>` +
			`<form hx-post="/develop/bot/investor/stop" hx-target="#investor-control" hx-swap="outerHTML">` +
			`<button type="submit" class="btn danger" style="font-size:10px;padding:5px 10px">Стоп</button></form></div>`
	}
	return `<div id="investor-control" hx-get="/develop/bot/investor/status" hx-trigger="every 10s" hx-swap="outerHTML" style="display:flex;align-items:center;gap:12px">` +
		`<span style="color:var(--muted);font-size:14px">○</span>` +
		`<span style="font-size:11px;color:var(--muted)">Остановлен</span>` +
		`<form hx-post="/develop/bot/investor/start" hx-target="#investor-control" hx-swap="outerHTML">` +
		`<button type="submit" class="btn primary" style="font-size:10px;padding:5px 10px">Запустить</button></form></div>`
}

// --- Bybit bot ---

func (h *Handler) BybitBotStatus(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, bybitStatusFragment(h.bot.IsBybitRunning(uid)))
}

func (h *Handler) BybitBotStart(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	h.bot.StartBybit(uid)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, bybitStatusFragment(true))
}

func (h *Handler) BybitBotStop(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	h.bot.StopBybit(uid)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, bybitStatusFragment(false))
}

func bybitStatusFragment(running bool) string {
	if running {
		return `<div id="bybit-control" hx-get="/develop/bot/bybit/status" hx-trigger="every 10s" hx-swap="outerHTML" style="display:flex;align-items:center;gap:12px">` +
			`<span style="color:var(--green);font-size:14px;animation:pulse 2s infinite">●</span>` +
			`<span style="font-size:11px;color:var(--green);font-weight:600">Активен</span>` +
			`<form hx-post="/develop/bot/bybit/stop" hx-target="#bybit-control" hx-swap="outerHTML">` +
			`<button type="submit" class="btn danger" style="font-size:10px;padding:5px 10px">Стоп</button></form></div>`
	}
	return `<div id="bybit-control" hx-get="/develop/bot/bybit/status" hx-trigger="every 10s" hx-swap="outerHTML" style="display:flex;align-items:center;gap:12px">` +
		`<span style="color:var(--muted);font-size:14px">○</span>` +
		`<span style="font-size:11px;color:var(--muted)">Остановлен</span>` +
		`<form hx-post="/develop/bot/bybit/start" hx-target="#bybit-control" hx-swap="outerHTML">` +
		`<button type="submit" class="btn primary" style="font-size:10px;padding:5px 10px">Запустить</button></form></div>`
}

func (h *Handler) AnalyzePortfolio(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	settings, _ := h.db.GetUserSettings(r.Context(), uid)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if settings.ClaudeAPIKey == "" {
		fmt.Fprint(w, `<div class="error-banner">⚠ Настройте Claude API Key выше и сохраните</div>`)
		return
	}

	assets, _ := h.db.GetAllAssets(r.Context(), uid)
	var posLines []string
	for _, a := range assets {
		if a.Type != models.AssetTypeInvest || a.Extra == "" || a.Extra == "null" {
			continue
		}
		var positions []models.InvestPosition
		if err := json.Unmarshal([]byte(a.Extra), &positions); err != nil {
			continue
		}
		for _, p := range positions {
			if p.Quantity > 0 {
				posLines = append(posLines, fmt.Sprintf("  %s: %.0f лот × %.0f ₽ = %.0f ₽",
					p.Ticker, p.Quantity, p.PriceRUB, p.ValueRUB))
			}
		}
	}

	if len(posLines) == 0 {
		fmt.Fprint(w, `<div class="error-banner">⚠ Нет данных о портфеле T-Инвестиции. Нажмите Обновить на главной странице.</div>`)
		return
	}

	riskNames := map[string]string{"low": "Низкий", "medium": "Средний", "high": "Высокий"}
	riskName := riskNames[settings.RiskLevel]
	if riskName == "" {
		riskName = "Средний"
	}

	prompt := fmt.Sprintf(`Ты — финансовый трейдер на на Московской бирже в РФ.

ПРОФИЛЬ РИСКА: %s
Максимально допустимая потеря: %.1f%% от портфеля

ТЕКУЩИЕ ПОЗИЦИИ (T-Инвестиции, Московская биржа):
%s

У тебя нет статуса квалифицированного инвестора.Дай 3-5 конкретных торговых рекомендаций. Учитывай профиль риска:
- Низкий: только ОФЗ, SBER, GAZP, LKOH, NLMK — крупнейшие голубые фишки
- Средний: добавляй ETF (TMOS, SBMX), 2-3 эшелон с хорошими фундаментальными показателями
- Высокий: технологии, малые компании, спекулятивные идеи, фьючерсы(кроме газа)

Укажи количество в ЛОТАХ (минимальная торговая единица на MOEX).
ВАЖНО: Ответь ТОЛЬКО JSON массивом без дополнительного текста:
[{"action":"BUY","ticker":"SBER","quantity":5,"reasoning":"Краткое обоснование на русском"}]`,
		riskName, settings.MaxLossPct, strings.Join(posLines, "\n"))

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	claudeClient := claude.New(settings.ClaudeAPIKey)
	resp, err := claudeClient.Ask(ctx, prompt)
	if err != nil {
		fmt.Fprintf(w, `<div class="error-banner">⚠ Ошибка Claude API: %s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	// Extract JSON array from response (Claude might wrap in markdown)
	jsonStr := resp
	if idx := strings.Index(resp, "["); idx >= 0 {
		if end := strings.LastIndex(resp, "]"); end > idx {
			jsonStr = resp[idx : end+1]
		}
	}

	var recs []models.TradeRecommendation
	if err := json.Unmarshal([]byte(jsonStr), &recs); err != nil {
		preview := jsonStr
		if len(preview) > 300 {
			preview = preview[:300] + "..."
		}
		fmt.Fprintf(w, `<div class="error-banner">⚠ Не удалось разобрать ответ Claude: %s</div>`, template.HTMLEscapeString(preview))
		return
	}

	canTrade := settings.TInvestTradeToken != ""

	fmt.Fprint(w, `<div class="rec-list">`)
	for _, rec := range recs {
		actionLabel := "▲ КУПИТЬ"
		actionColor := "var(--green)"
		direction := "ORDER_DIRECTION_BUY"
		if rec.Action == "SELL" {
			actionLabel = "▼ ПРОДАТЬ"
			actionColor = "var(--red)"
			direction = "ORDER_DIRECTION_SELL"
		}
		_ = direction

		tradeBtn := ""
		if canTrade {
			confirm := fmt.Sprintf("Выполнить сделку: %s %s %d лот?", rec.Action, rec.Ticker, rec.Quantity)
			tradeBtn = fmt.Sprintf(`
<form hx-post="/develop/trade" hx-target="closest .rec-card" hx-swap="outerHTML" style="margin-top:10px">
  <input type="hidden" name="ticker" value="%s">
  <input type="hidden" name="action" value="%s">
  <input type="hidden" name="quantity" value="%d">
  <button type="submit" class="btn primary" style="font-size:10px" onclick="return confirm(%q)">Выполнить</button>
</form>`,
				template.HTMLEscapeString(rec.Ticker), template.HTMLEscapeString(rec.Action), rec.Quantity, confirm)
		}

		fmt.Fprintf(w, `
<div class="rec-card card" style="margin-bottom:12px">
  <div style="display:flex; align-items:center; gap:12px; margin-bottom:8px">
    <span style="color:%s; font-weight:600; font-size:11px">%s</span>
    <span style="font-family:var(--display); font-size:16px; font-weight:600; color:var(--text)">%s</span>
    <span style="color:var(--subtext); font-size:12px">%d лот</span>
  </div>
  <div style="font-size:12px; color:var(--subtext); line-height:1.5">%s</div>
  %s
</div>`,
			actionColor, actionLabel,
			template.HTMLEscapeString(rec.Ticker),
			rec.Quantity,
			template.HTMLEscapeString(rec.Reasoning),
			tradeBtn,
		)
	}
	fmt.Fprint(w, `</div>`)
}

func (h *Handler) ExecuteTrade(w http.ResponseWriter, r *http.Request) {
	uid := ctxUserID(r)
	settings, _ := h.db.GetUserSettings(r.Context(), uid)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if settings.TInvestTradeToken == "" {
		fmt.Fprint(w, `<div class="rec-card error-banner" style="font-size:11px">⚠ Нет торгового токена T-Инвестиции</div>`)
		return
	}

	ticker := strings.ToUpper(strings.TrimSpace(r.FormValue("ticker")))
	action := r.FormValue("action")
	qty, _ := strconv.Atoi(r.FormValue("quantity"))

	if ticker == "" || qty <= 0 || (action != "BUY" && action != "SELL") {
		fmt.Fprint(w, `<div class="rec-card error-banner" style="font-size:11px">⚠ Неверные параметры сделки</div>`)
		return
	}

	direction := "ORDER_DIRECTION_BUY"
	if action == "SELL" {
		direction = "ORDER_DIRECTION_SELL"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	tc := tinvest.New(settings.TInvestTradeToken)

	accountID, err := tc.GetFirstAccountID(ctx)
	if err != nil {
		fmt.Fprintf(w, `<div class="rec-card error-banner" style="font-size:11px">⚠ Не удалось получить счёт: %s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	figi, err := tc.FindFIGI(ctx, ticker)
	if err != nil {
		fmt.Fprintf(w, `<div class="rec-card error-banner" style="font-size:11px">⚠ Инструмент не найден: %s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	orderID, err := tc.PlaceMarketOrder(ctx, accountID, figi, qty, direction)
	if err != nil {
		fmt.Fprintf(w, `<div class="rec-card error-banner" style="font-size:11px">⚠ Ошибка ордера: %s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	actionWord := "куплено"
	if action == "SELL" {
		actionWord = "продано"
	}
	slog.Info("trade executed", "ticker", ticker, "action", action, "qty", qty, "orderID", orderID, "userID", uid)

	fmt.Fprintf(w, `
<div class="rec-card card" style="border-color:var(--green); margin-bottom:12px">
  <div style="color:var(--green); font-weight:600">✓ Ордер выставлен</div>
  <div style="font-size:12px; color:var(--subtext); margin-top:4px">
    %s: %s × %d лот | ID: %s
  </div>
</div>`,
		actionWord, template.HTMLEscapeString(ticker), qty, template.HTMLEscapeString(orderID))
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"fmtRUB": func(v float64) string {
			neg := v < 0
			if neg {
				v = -v
			}
			s := fmt.Sprintf("%.0f", v)
			n := len(s)
			out := make([]byte, 0, n+(n-1)/3+4)
			for i := range s {
				if i > 0 && (n-i)%3 == 0 {
					out = append(out, '\xc2', '\xa0') // U+00A0 non-breaking space
				}
				out = append(out, s[i])
			}
			if neg {
				return "-" + string(out) + " ₽"
			}
			return string(out) + " ₽"
		},
		"fmtCrypto": func(v float64, cur string) string {
			return fmt.Sprintf("%.8f %s", v, cur)
		},
		"fmtFloat": func(v float64) string {
			return fmt.Sprintf("%.2f", v)
		},
		"add": func(a, b float64) float64 {
			return a + b
		},
		"mul": func(a float64, b int) float64 {
			return a * float64(b)
		},
		"fmtInstrumentType": func(t string) string {
			switch t {
			case "share":
				return "Акции"
			case "etf":
				return "ETF"
			case "bond":
				return "Облигации"
			case "currency":
				return "Валюта"
			case "future":
				return "Фьючерс"
			case "option":
				return "Опцион"
			default:
				return t
			}
		},
		"sourceBase": func(src string) string {
			for _, prefix := range []string{"steam", "monero", "bitcoin", "bybit", "tbank", "tinvest", "manual"} {
				if strings.HasPrefix(src, prefix) {
					return prefix
				}
			}
			return src
		},
		"assetKey": func(source, name string) string {
			return source + ":" + name
		},
		"isHidden": func(hiddenMap map[string]bool, source, name string) bool {
			if hiddenMap == nil {
				return false
			}
			return hiddenMap[source+":"+name]
		},
	}
}
