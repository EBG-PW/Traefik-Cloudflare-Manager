package api

import (
	"crypto/sha256"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	cfg := a.currentConfig()
	if cfg == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		a.render(w, "login.tmpl", pageView{Title: "Login"})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		a.render(w, "login.tmpl", pageView{Title: "Login", Error: "Could not read login form."})
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	key := clientIP(r) + "|" + strings.ToLower(username)
	if a.loginBlocked(key) {
		a.render(w, "login.tmpl", pageView{Title: "Login", Error: "Login failed."})
		return
	}
	if !userMatches(cfg, username, password) {
		a.recordLoginFailure(key)
		a.render(w, "login.tmpl", pageView{Title: "Login", Error: "Login failed."})
		return
	}
	a.clearLoginFailures(key)
	a.upgradePasswordHash(username, password)
	a.startSession(w, r, username)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c, err := r.Cookie(sessionCookie)
	if err == nil {
		a.mu.Lock()
		delete(a.sessions, sha256.Sum256([]byte(c.Value)))
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: "", Path: "/", MaxAge: -1, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) loginBlocked(key string) bool {
	now := time.Now().UTC()
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := a.loginAttempts[key]
	if attempt.First.IsZero() || now.Sub(attempt.First) > 15*time.Minute {
		delete(a.loginAttempts, key)
		return false
	}
	return attempt.Failures >= 5
}

func (a *App) recordLoginFailure(key string) {
	now := time.Now().UTC()
	a.mu.Lock()
	defer a.mu.Unlock()
	attempt := a.loginAttempts[key]
	if attempt.First.IsZero() || now.Sub(attempt.First) > 15*time.Minute {
		attempt = loginAttempt{First: now}
	}
	attempt.Failures++
	a.loginAttempts[key] = attempt
}

func (a *App) clearLoginFailures(key string) {
	a.mu.Lock()
	delete(a.loginAttempts, key)
	a.mu.Unlock()
}

func (a *App) upgradePasswordHash(username, password string) {
	cfg := a.currentConfig()
	for _, user := range lib.TraefikUsers(cfg) {
		if user.Username != username {
			continue
		}
		cost, err := bcrypt.Cost([]byte(user.PasswordHash))
		if err != nil || cost >= 12 {
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
		if err != nil {
			return
		}
		_, err = a.jsonStore.UpdateConfig(func(current *models.Config) error {
			for i := range current.Users {
				if current.Users[i].Username == username {
					current.Users[i].PasswordHash = string(hash)
				}
			}
			if current.Username == username {
				current.PasswordHash = string(hash)
			}
			return nil
		})
		if err == nil {
			_ = a.writeCurrentTraefikConfig()
		}
		return
	}
}
