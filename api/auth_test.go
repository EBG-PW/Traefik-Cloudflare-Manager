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

func TestBasicAuthIsNotAcceptedAndDoesNotBypassCSRF(t *testing.T) {
	app := &App{
		cfg:      &models.Config{Users: []models.User{{Username: "admin", PasswordHash: "unused"}}},
		sessions: make(map[[32]byte]session),
	}
	request := httptest.NewRequest(http.MethodPost, "https://manager.example.com/api/proxies", nil)
	request.SetBasicAuth("admin", "password")
	if got := app.currentUsername(request); got != "" {
		t.Fatalf("Basic Auth returned session user %q", got)
	}
	if app.validCSRF(request) {
		t.Fatal("Basic Auth bypassed CSRF validation")
	}
}

func TestForwardedHTTPSIsAcceptedOnlyFromPrivateProxy(t *testing.T) {
	privateRequest := httptest.NewRequest(http.MethodGet, "http://manager/dashboard", nil)
	privateRequest.RemoteAddr = "192.168.65.4:43210"
	privateRequest.Header.Set("X-Forwarded-Proto", "https")
	if !requestIsHTTPS(privateRequest) {
		t.Fatal("HTTPS forwarded by a private Traefik address was not detected")
	}

	publicRequest := httptest.NewRequest(http.MethodGet, "http://manager/dashboard", nil)
	publicRequest.RemoteAddr = "203.0.113.10:43210"
	publicRequest.Header.Set("X-Forwarded-Proto", "https")
	if requestIsHTTPS(publicRequest) {
		t.Fatal("public client was allowed to spoof forwarded HTTPS")
	}
}
