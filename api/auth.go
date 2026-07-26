package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

type authenticatedUserContextKey struct{}

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := a.currentUsername(r)
		if username == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !a.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), authenticatedUserContextKey{}, username)))
	}
}

func (a *App) requireAuthHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username := a.currentUsername(r)
		if username == "" {
			if wantsJSON(r) {
				apiError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !a.validCSRF(r) {
			apiError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authenticatedUserContextKey{}, username)))
	})
}

func (a *App) currentUsername(r *http.Request) string {
	if username, ok := r.Context().Value(authenticatedUserContextKey{}).(string); ok {
		return username
	}
	users := a.currentUsers()
	if user, pass, ok := r.BasicAuth(); ok {
		if userMatchesUsers(users, user, pass) {
			return user
		}
		return ""
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	key := sha256.Sum256([]byte(c.Value))
	now := time.Now().UTC()
	a.mu.Lock()
	sess, ok := a.sessions[key]
	if ok && (now.Sub(sess.LastSeen) > 30*time.Minute || now.Sub(sess.Created) > 12*time.Hour) {
		delete(a.sessions, key)
		ok = false
	}
	if ok {
		sess.LastSeen = now
		a.sessions[key] = sess
	}
	a.mu.Unlock()
	if !ok {
		return ""
	}
	sessionUser := sess.Username
	for _, user := range users {
		if subtle.ConstantTimeCompare([]byte(sessionUser), []byte(user.Username)) == 1 {
			return sessionUser
		}
	}
	return ""
}

func (a *App) validCSRF(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	if _, _, ok := r.BasicAuth(); ok {
		return true
	}
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		_ = r.ParseForm()
		token = r.FormValue("csrf_token")
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) == 1
}

func (a *App) currentUsers() []models.User {
	if a.jsonStore != nil {
		return a.jsonStore.Users()
	}
	cfg := a.currentConfig()
	if cfg == nil {
		return nil
	}
	return lib.TraefikUsers(cfg)
}

func userMatches(cfg *models.Config, username, password string) bool {
	if cfg == nil {
		return false
	}
	return userMatchesUsers(lib.TraefikUsers(cfg), username, password)
}

func userMatchesUsers(users []models.User, username, password string) bool {
	for _, user := range users {
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
