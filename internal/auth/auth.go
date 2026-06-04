package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type ctxKey string

const userKey ctxKey = "user"

type Auth struct {
	secret        []byte
	adminUsername string
	adminPassword string
}

func New(secret, username, password string) *Auth {
	return &Auth{
		secret:        []byte(secret),
		adminUsername: username,
		adminPassword: password,
	}
}

func (a *Auth) Validate(username, password string) bool {
	return username == a.adminUsername && password == a.adminPassword
}

func (a *Auth) GenerateToken(username string) (string, error) {
	claims := jwt.MapClaims{
		"sub": username,
		"exp": time.Now().Add(24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.secret)
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("sf_token")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
			return a.secret, nil
		}, jwt.WithValidMethods([]string{"HS256"}))

		if err != nil || !token.Valid {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		ctx := context.WithValue(r.Context(), userKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
