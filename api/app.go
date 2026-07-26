package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

const sessionCookie = "tcm_session"
const csrfCookie = "tcm_csrf"

type session struct {
	Username string
	Created  time.Time
	LastSeen time.Time
}

type loginAttempt struct {
	Failures int
	First    time.Time
}

//go:embed templates/*.tmpl
var templatesFS embed.FS

var templateFunctions = template.FuncMap{
	"bytes":         lib.FormatBytes,
	"strategyLabel": lib.LoadBalancerStrategyLabel,
	"certTime":      lib.FormatCertTime,
	"certDuration":  lib.FormatCertDuration,
	"add":           func(a, b int) int { return a + b },
	"nonce":         func() string { return "" },
}

var parsedTemplates = template.Must(template.New("").Funcs(templateFunctions).ParseFS(templatesFS, "templates/*.tmpl"))

type App struct {
	mu                sync.RWMutex
	cfg               *models.Config
	store             *models.Store
	jsonStore         *lib.JSONStore
	cf                *lib.CloudflareClient
	docker            *lib.DockerClient
	sessions          map[[32]byte]session
	loginAttempts     map[string]loginAttempt
	setupJobs         map[string]*setupJob
	proxyChecks       map[string]bool
	reconcilerStarted bool
	reconcileMu       sync.Mutex
	statsMu           sync.Mutex
	statsHist         []statsSample
	versionMu         sync.Mutex
	versionInfo       models.TraefikVersionInfo
	versionNextCheck  time.Time
	versionChecking   bool
	versionGeneration uint64
}

func NewApp(store *models.Store, cfg *models.Config) *App {
	jsonStore, err := lib.OpenJSONStore(store)
	if err != nil {
		panic(err)
	}
	return NewAppWithJSONStore(store, jsonStore, cfg)
}

func NewAppWithJSONStore(store *models.Store, jsonStore *lib.JSONStore, cfg *models.Config) *App {
	token := ""
	if cfg != nil {
		token = cfg.CloudflareToken
	}
	app := &App{
		cfg:           cfg,
		store:         store,
		jsonStore:     jsonStore,
		cf:            lib.NewCloudflareClient(token),
		docker:        lib.NewDockerClient(store.DockerSocket),
		sessions:      map[[32]byte]session{},
		loginAttempts: map[string]loginAttempt{},
		setupJobs:     map[string]*setupJob{},
		proxyChecks:   map[string]bool{},
	}
	if cfg != nil {
		for _, proxy := range cfg.Proxies {
			if proxy.Paused || proxy.SSLReady {
				continue
			}
			app.scheduleProxyReadyCheck(proxy.Host)
		}
		app.ensureProxyReconciler()
	}
	return app
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleRoot)
	mux.HandleFunc("/setup", a.handleSetup)
	mux.HandleFunc("/setup/start", a.handleSetupStart)
	mux.HandleFunc("/setup/status", a.handleSetupStatus)
	mux.HandleFunc("/login", a.handleLogin)
	mux.HandleFunc("/logout", a.requireAuth(a.handleLogout))
	mux.HandleFunc("/dashboard", a.requireAuth(a.handleDashboard))
	mux.HandleFunc("/traefik", a.requireAuth(a.handleTraefikPage))
	mux.HandleFunc("/proxies", a.requireAuth(a.handleProxyCreate))
	mux.HandleFunc("/proxies/delete", a.requireAuth(a.handleProxyDelete))
	mux.HandleFunc("/users", a.requireAuth(a.handleUserCreate))
	mux.HandleFunc("/users/delete", a.requireAuth(a.handleUserDelete))
	mux.HandleFunc("/users/password", a.requireAuth(a.handlePasswordChange))
	mux.HandleFunc("/users/reset", a.requireAuth(a.handlePasswordReset))
	mux.HandleFunc("/traefik/redeploy", a.requireAuth(a.handleRedeploy))
	mux.HandleFunc("/traefik/stop", a.requireAuth(a.handleStopTraefik))

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/api/config", a.handleAPIConfig)
	apiMux.HandleFunc("/api/users", a.handleAPIUsers)
	apiMux.HandleFunc("/api/users/", a.handleAPIUserByName)
	apiMux.HandleFunc("/api/proxies", a.handleAPIProxies)
	apiMux.HandleFunc("/api/proxies/", a.handleAPIProxyByHost)
	apiMux.HandleFunc("/api/traefik/redeploy", a.handleAPIRedeploy)
	apiMux.HandleFunc("/api/traefik/stop", a.handleAPIStopTraefik)
	apiMux.HandleFunc("/api/traefik/logs", a.handleAPITraefikLogs)
	apiMux.HandleFunc("/api/traefik/logs/ws", a.handleAPITraefikLogsWS)
	apiMux.HandleFunc("/api/traefik/stats/ws", a.handleAPIStatsWS)
	apiMux.HandleFunc("/api/traefik/stats", a.handleAPIStats)
	mux.Handle("/api/", a.requireAuthHandler(apiMux))

	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return a.enforceTransport(mux)
}

func (a *App) currentConfig() *models.Config {
	if a.jsonStore != nil {
		cfg, err := a.jsonStore.LoadConfig()
		if err == nil {
			return cfg
		}
		log.Printf("load current config: %v", err)
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cfg == nil {
		return nil
	}
	cp := *a.cfg
	cp.Users = append([]models.User(nil), a.cfg.Users...)
	cp.Proxies = append([]models.ProxyConfig(nil), a.cfg.Proxies...)
	return &cp
}

func (a *App) setConfig(cfg *models.Config) {
	a.mu.Lock()
	lib.NormalizeConfig(cfg)
	a.cfg = cfg
	a.cf = lib.NewCloudflareClient(cfg.CloudflareToken)
	a.mu.Unlock()
	a.ensureProxyReconciler()
}

func (a *App) ensureProxyReconciler() {
	a.mu.Lock()
	if a.reconcilerStarted || a.cfg == nil {
		a.mu.Unlock()
		return
	}
	a.reconcilerStarted = true
	a.mu.Unlock()
	go a.startProxyReconciler()
}

func (a *App) startSession(w http.ResponseWriter, r *http.Request, username string) {
	tokenBytes := make([]byte, 32)
	_, _ = rand.Read(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	now := time.Now().UTC()
	a.mu.Lock()
	if existing, err := r.Cookie(sessionCookie); err == nil {
		delete(a.sessions, sha256.Sum256([]byte(existing.Value)))
	}
	a.sessions[sha256.Sum256([]byte(token))] = session{Username: username, Created: now, LastSeen: now}
	a.mu.Unlock()
	secure := requestIsHTTPS(r)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 60 * 60})
	csrfBytes := make([]byte, 32)
	_, _ = rand.Read(csrfBytes)
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: base64.RawURLEncoding.EncodeToString(csrfBytes), Path: "/", HttpOnly: false, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 60 * 60})
}

func (a *App) revokeUserSessions(username string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for token, sess := range a.sessions {
		if sess.Username == username {
			delete(a.sessions, token)
		}
	}
}

func (a *App) writeCurrentTraefikConfig() error {
	cfg, err := a.jsonStore.LoadConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		return errors.New("configuration is not initialized")
	}
	return lib.WriteTraefikConfig(a.store, cfg)
}

func (a *App) render(w http.ResponseWriter, name string, data any) {
	nonceBytes := make([]byte, 18)
	_, _ = rand.Read(nonceBytes)
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; font-src 'self' data: https://cdn.jsdelivr.net; style-src 'self' 'nonce-"+nonce+"' https://cdn.jsdelivr.net; script-src 'self' 'nonce-"+nonce+"' https://cdn.jsdelivr.net; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	t, err := parsedTemplates.Clone()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	t.Funcs(template.FuncMap{"nonce": func() string { return nonce }})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render: %v", err)
	}
}

func (a *App) deployTraefik(ctx context.Context, cfg *models.Config) error {
	if err := a.docker.Ping(ctx); err != nil {
		return err
	}
	if err := a.docker.EnsureNetwork(ctx, a.store.DockerNetwork); err != nil {
		return err
	}
	if a.store.DockerVolume != "" {
		if err := a.docker.EnsureVolume(ctx, a.store.DockerVolume); err != nil {
			return err
		}
	}
	hostname, _ := os.Hostname()
	if hostname != "" {
		_ = a.docker.ConnectNetwork(ctx, a.store.DockerNetwork, hostname, "traefik-cloudflare-manager", "manager")
	}
	if err := a.docker.PullImage(ctx, a.store.TraefikImage); err != nil {
		return err
	}
	_ = a.docker.RemoveContainer(ctx, "traefik")
	return a.docker.CreateTraefik(ctx, cfg, a.store)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
