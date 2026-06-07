package auth

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type ctxKey string

const userKey ctxKey = "user"

type Auth struct {
	secret []byte
}

func New(secret string) *Auth {
	return &Auth{secret: []byte(secret)}
}

func (a *Auth) GenerateToken(username string) (string, error) {
	claims := jwt.MapClaims{
		"sub": username,
		"exp": time.Now().Add(24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.secret)
}

func (a *Auth) ValidateToken(tokenString string) bool {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return a.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	return err == nil && token.Valid
}

// GetUsername extracts the username from a request authenticated via Middleware.
func GetUsername(r *http.Request) string {
	tok, _ := r.Context().Value(userKey).(*jwt.Token)
	if tok == nil {
		return ""
	}
	claims, _ := tok.Claims.(jwt.MapClaims)
	sub, _ := claims["sub"].(string)
	return sub
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("sf_token")
		if err != nil {
			slog.Info("auth middleware: missing cookie", "err", err)
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
			return a.secret, nil
		}, jwt.WithValidMethods([]string{"HS256"}))

		if err != nil || !token.Valid {
			slog.Info("auth middleware: invalid token", "err", err)
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		ctx := context.WithValue(r.Context(), userKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
