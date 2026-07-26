package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

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
		if !a.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
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
		if !a.validCSRF(r) {
			apiError(w, http.StatusForbidden, "invalid CSRF token")
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
	for _, user := range lib.TraefikUsers(cfg) {
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
