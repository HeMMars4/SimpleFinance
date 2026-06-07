package handlers

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/HeMMars4/simple-finance/internal/auth"
	"github.com/HeMMars4/simple-finance/internal/storage"
)

type Handler struct {
	agg       *Aggregator
	auth      *auth.Auth
	db        *storage.DB
	templates *template.Template
}

func New(agg *Aggregator, a *auth.Auth, db *storage.DB, templateDir string) *Handler {
	pattern := filepath.Join(templateDir, "*.html")
	tmpl := template.Must(template.New("").Funcs(templateFuncs()).ParseGlob(pattern))
	return &Handler{agg: agg, auth: a, db: db, templates: tmpl}
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login.html", nil)
}

func (h *Handler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if !h.auth.Validate(r.FormValue("username"), r.FormValue("password")) {
		h.render(w, "login.html", map[string]string{"Error": "Неверный логин или пароль"})
		return
	}
	token, err := h.auth.GenerateToken(r.FormValue("username"))
	if err != nil {
		http.Error(w, "token error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "sf_token",
		Value:    token,
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "sf_token", Value: "", MaxAge: -1, Path: "/"})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	data, err := h.agg.BuildDashboard(r.Context())
	if err != nil {
		data.Error = err.Error()
	}
	h.render(w, "dashboard.html", data)
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	err := h.agg.RefreshAll(r.Context())
	data, _ := h.agg.BuildDashboard(r.Context())
	if err != nil {
		data.Error = "Частичное обновление — некоторые источники недоступны"
	}
	h.render(w, "dashboard_content.html", data)
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"fmtRUB": func(v float64) string {
			if v >= 1_000_000 {
				return fmt.Sprintf("%.2f М ₽", v/1_000_000)
			}
			if v >= 1_000 {
				return fmt.Sprintf("%.2f К ₽", v/1_000)
			}
			return fmt.Sprintf("%.2f ₽", v)
		},
		"fmtCrypto": func(v float64, cur string) string {
			return fmt.Sprintf("%.8f %s", v, cur)
		},
		"fmtFloat": func(v float64) string {
			return fmt.Sprintf("%.2f", v)
		},
	}
}
