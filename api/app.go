package api

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"sync"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

const sessionCookie = "tcm_session"

//go:embed templates/*.tmpl
var templatesFS embed.FS

type App struct {
	mu                sync.RWMutex
	cfg               *models.Config
	store             *models.Store
	cf                *lib.CloudflareClient
	docker            *lib.DockerClient
	sessions          map[string]string
	setupJobs         map[string]*setupJob
	proxyChecks       map[string]bool
	reconcilerStarted bool
	reconcileMu       sync.Mutex
	statsMu           sync.Mutex
	statsHist         []statsSample
}

func NewApp(store *models.Store, cfg *models.Config) *App {
	token := ""
	if cfg != nil {
		token = cfg.CloudflareToken
	}
	app := &App{
		cfg:         cfg,
		store:       store,
		cf:          lib.NewCloudflareClient(token),
		docker:      lib.NewDockerClient(store.DockerSocket),
		sessions:    map[string]string{},
		setupJobs:   map[string]*setupJob{},
		proxyChecks: map[string]bool{},
	}
	if cfg != nil {
		for _, proxy := range cfg.Proxies {
			if proxy.Paused || proxy.Status == "ready" {
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
	apiMux.HandleFunc("/api/traefik/command", a.handleAPITraefikCommand)
	apiMux.HandleFunc("/api/traefik/stats/ws", a.handleAPIStatsWS)
	apiMux.HandleFunc("/api/traefik/stats", a.handleAPIStats)
	mux.Handle("/api/", a.requireAuthHandler(apiMux))

	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func (a *App) currentConfig() *models.Config {
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

func (a *App) startSession(w http.ResponseWriter, username string) {
	tokenBytes := make([]byte, 32)
	_, _ = rand.Read(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	a.mu.Lock()
	a.sessions[token] = username
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
}

func (a *App) render(w http.ResponseWriter, name string, data any) {
	t, err := template.New("").Funcs(template.FuncMap{
		"bytes":         lib.FormatBytes,
		"strategyLabel": lib.LoadBalancerStrategyLabel,
		"certTime":      lib.FormatCertTime,
		"certDuration":  lib.FormatCertDuration,
		"add":           func(a, b int) int { return a + b },
	}).ParseFS(templatesFS, "templates/*.tmpl")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
