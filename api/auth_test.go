package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"traefik-cloudflare-manager/models"
)

func TestSessionCookiesAndCSRF(t *testing.T) {
	app := &App{
		cfg:           &models.Config{Users: []models.User{{Username: "admin", PasswordHash: "unused"}}},
		sessions:      make(map[[32]byte]session),
		loginAttempts: make(map[string]loginAttempt),
	}
	loginRequest := httptest.NewRequest(http.MethodPost, "https://manager.example.com/login", nil)
	loginRequest.TLS = &tls.ConnectionState{}
	recorder := httptest.NewRecorder()
	app.startSession(recorder, loginRequest, "admin")

	var sessionValue, csrfValue string
	for _, cookie := range recorder.Result().Cookies() {
		switch cookie.Name {
		case sessionCookie:
			sessionValue = cookie.Value
			if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("unsafe session cookie: %#v", cookie)
			}
		case csrfCookie:
			csrfValue = cookie.Value
			if cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("unexpected CSRF cookie flags: %#v", cookie)
			}
		}
	}
	if sessionValue == "" || csrfValue == "" {
		t.Fatal("session cookies were not created")
	}

	request := httptest.NewRequest(http.MethodPost, "https://manager.example.com/api/proxies", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionValue})
	request.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrfValue})
	if got := app.currentUsername(request); got != "admin" {
		t.Fatalf("unexpected session user %q", got)
	}
	if app.validCSRF(request) {
		t.Fatal("request without CSRF header was accepted")
	}
	request.Header.Set("X-CSRF-Token", csrfValue)
	if !app.validCSRF(request) {
		t.Fatal("matching CSRF header was rejected")
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	app := &App{
		cfg:      &models.Config{Users: []models.User{{Username: "admin"}}},
		sessions: make(map[[32]byte]session),
	}
	request := httptest.NewRequest(http.MethodGet, "http://manager.local/dashboard", nil)
	recorder := httptest.NewRecorder()
	app.startSession(recorder, request, "admin")
	sessionValue := recorder.Result().Cookies()[0].Value
	for key, value := range app.sessions {
		value.LastSeen = time.Now().UTC().Add(-31 * time.Minute)
		app.sessions[key] = value
	}
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionValue})
	if got := app.currentUsername(request); got != "" {
		t.Fatalf("expired session returned user %q", got)
	}
}
