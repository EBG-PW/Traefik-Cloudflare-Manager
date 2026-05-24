package api

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (a *App) requireAuthHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.authenticated(r) {
			if wantsJSON(r) {
				apiError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) authenticated(r *http.Request) bool {
	return a.currentUsername(r) != ""
}

func (a *App) currentUsername(r *http.Request) string {
	cfg := a.currentConfig()
	if cfg == nil {
		return ""
	}
	if user, pass, ok := r.BasicAuth(); ok {
		if userMatches(cfg, user, pass) {
			return user
		}
		return ""
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	a.mu.RLock()
	sessionUser := a.sessions[c.Value]
	a.mu.RUnlock()
	for _, user := range lib.TraefikUsers(cfg) {
		if subtle.ConstantTimeCompare([]byte(sessionUser), []byte(user.Username)) == 1 {
			return sessionUser
		}
	}
	return ""
}

func userMatches(cfg *models.Config, username, password string) bool {
	for _, user := range lib.TraefikUsers(cfg) {
		if subtle.ConstantTimeCompare([]byte(username), []byte(user.Username)) == 1 &&
			bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil {
			return true
		}
	}
	return false
}

func wantsJSON(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/")
}
