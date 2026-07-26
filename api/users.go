package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

type userInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *App) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfg := a.currentConfig()
		a.render(w, "users.tmpl", usersView{
			Config:      cfg,
			Message:     r.URL.Query().Get("msg"),
			Error:       r.URL.Query().Get("err"),
			CurrentUser: a.currentUsername(r),
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := parseRequestForm(r); err != nil {
		redirectUsersErr(w, r, "Could not read user form.")
		return
	}
	if err := a.addUser(userInput{Username: r.FormValue("username"), Password: r.FormValue("password")}); err != nil {
		redirectUsersErr(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/users?msg="+url.QueryEscape("User added."), http.StatusSeeOther)
}

func (a *App) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := parseRequestForm(r); err != nil {
		redirectUsersErr(w, r, "Could not read delete request.")
		return
	}
	if err := a.deleteUser(r.FormValue("username"), a.currentUsername(r)); err != nil {
		redirectUsersErr(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/users?msg="+url.QueryEscape("User removed."), http.StatusSeeOther)
}

func (a *App) handleAPIUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		users := safeUsers(a.currentConfig().Users)
		writeJSON(w, http.StatusOK, users)
	case http.MethodPost:
		var input userInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := a.addUser(input); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAPIUserByName(w http.ResponseWriter, r *http.Request) {
	username := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/users/"), "/")
	if username == "" {
		apiError(w, http.StatusBadRequest, "username is required")
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if err := a.deleteUser(username, a.currentUsername(r)); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	case http.MethodPut:
		var input userInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if err := a.setUserPassword(username, input.Password); err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.revokeUserSessions(username)
		writeJSON(w, http.StatusOK, map[string]string{"status": "password_updated"})
	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) addUser(input userInput) error {
	username := strings.TrimSpace(input.Username)
	if username == "" || strings.Contains(username, ":") {
		return errString("Username is required and cannot contain ':'.")
	}
	if len(input.Password) < 12 {
		return errString("Password must be at least 12 characters.")
	}
	cfg := a.currentConfig()
	for _, user := range lib.TraefikUsers(cfg) {
		if strings.EqualFold(user.Username, username) {
			return errString("User already exists.")
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
	if err != nil {
		return errString("Could not hash password.")
	}
	cfg, err = a.jsonStore.UpdateConfig(func(current *models.Config) error {
		for _, user := range lib.TraefikUsers(current) {
			if strings.EqualFold(user.Username, username) {
				return errString("User already exists.")
			}
		}
		current.Users = append(lib.TraefikUsers(current), models.User{Username: username, PasswordHash: string(hash), CreatedAt: time.Now().UTC()})
		return nil
	})
	if err != nil {
		return errString("Could not save config: " + err.Error())
	}
	if err := a.writeCurrentTraefikConfig(); err != nil {
		return errString("Could not write Traefik config: " + err.Error())
	}
	return nil
}

func (a *App) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectUsersErr(w, r, "Could not read password form.")
		return
	}
	username := a.currentUsername(r)
	if !userMatches(a.currentConfig(), username, r.FormValue("current_password")) {
		redirectUsersErr(w, r, "Current password is incorrect.")
		return
	}
	if err := a.setUserPassword(username, r.FormValue("new_password")); err != nil {
		redirectUsersErr(w, r, err.Error())
		return
	}
	a.revokeUserSessions(username)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) handlePasswordReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectUsersErr(w, r, "Could not read reset form.")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	if err := a.setUserPassword(username, r.FormValue("new_password")); err != nil {
		redirectUsersErr(w, r, err.Error())
		return
	}
	a.revokeUserSessions(username)
	http.Redirect(w, r, "/users?msg="+url.QueryEscape("Password reset. Existing sessions were revoked."), http.StatusSeeOther)
}

func (a *App) setUserPassword(username, password string) error {
	if len(password) < 12 {
		return errString("Password must be at least 12 characters.")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return errString("Could not hash password.")
	}
	found := false
	_, err = a.jsonStore.UpdateConfig(func(current *models.Config) error {
		for i := range current.Users {
			if current.Users[i].Username == username {
				current.Users[i].PasswordHash = string(hash)
				found = true
			}
		}
		if !found {
			return errString("User not found.")
		}
		if current.Username == username {
			current.PasswordHash = string(hash)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := a.writeCurrentTraefikConfig(); err != nil {
		return errString("Could not write Traefik config: " + err.Error())
	}
	a.revokeUserSessions(username)
	return nil
}

func (a *App) deleteUser(username, currentUser string) error {
	username = strings.TrimSpace(username)
	cfg := a.currentConfig()
	users := lib.TraefikUsers(cfg)
	if len(users) <= 1 {
		return errString("Cannot remove the last user.")
	}
	if username == currentUser {
		return errString("Cannot remove the user you are currently logged in as.")
	}
	next := make([]models.User, 0, len(users)-1)
	found := false
	for _, user := range users {
		if user.Username == username {
			found = true
			continue
		}
		next = append(next, user)
	}
	if !found {
		return errString("User not found.")
	}
	cfg, err := a.jsonStore.UpdateConfig(func(current *models.Config) error {
		currentUsers := lib.TraefikUsers(current)
		if len(currentUsers) <= 1 {
			return errString("Cannot remove the last user.")
		}
		freshNext := make([]models.User, 0, len(currentUsers)-1)
		foundCurrent := false
		for _, user := range currentUsers {
			if user.Username == username {
				foundCurrent = true
				continue
			}
			freshNext = append(freshNext, user)
		}
		if !foundCurrent {
			return errString("User not found.")
		}
		current.Users = freshNext
		if current.Username == username && len(freshNext) > 0 {
			current.Username = freshNext[0].Username
			current.PasswordHash = freshNext[0].PasswordHash
		}
		return nil
	})
	if err != nil {
		return errString("Could not save config: " + err.Error())
	}
	if err := a.writeCurrentTraefikConfig(); err != nil {
		return errString("Could not write Traefik config: " + err.Error())
	}
	a.revokeUserSessions(username)
	return nil
}

func safeUsers(users []models.User) []map[string]any {
	out := make([]map[string]any, 0, len(users))
	for _, user := range users {
		out = append(out, map[string]any{"username": user.Username, "created_at": user.CreatedAt})
	}
	return out
}

func redirectUsersErr(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/users?err="+url.QueryEscape(msg), http.StatusSeeOther)
}
