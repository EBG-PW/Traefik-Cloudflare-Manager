package api

import (
	"net/http"
	"strings"
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
	if !userMatches(cfg, username, password) {
		a.render(w, "login.tmpl", pageView{Title: "Login", Error: "Login failed."})
		return
	}
	a.startSession(w, username)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
