package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if a.currentConfig() == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (a *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	if a.currentConfig() != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		a.render(w, "setup.tmpl", defaultSetupView(""))
		return
	}
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

func (a *App) handleSetupStart(w http.ResponseWriter, r *http.Request) {
	if a.currentConfig() != nil {
		apiError(w, http.StatusConflict, "setup is already complete")
		return
	}
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := parseRequestForm(r); err != nil {
		apiError(w, http.StatusBadRequest, "could not read setup form")
		return
	}
	cfg, password, err := setupConfigFromForm(r)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	job := newSetupJob()
	a.mu.Lock()
	a.setupJobs[job.ID] = job
	a.mu.Unlock()
	go a.runSetupJob(job, cfg, password)
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

func (a *App) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	a.mu.RLock()
	job := a.setupJobs[id]
	a.mu.RUnlock()
	if job == nil {
		apiError(w, http.StatusNotFound, "setup job was not found")
		return
	}
	writeJSON(w, http.StatusOK, job.snapshot())
}

func setupConfigFromForm(r *http.Request) (*models.Config, string, error) {
	token := strings.TrimSpace(r.FormValue("cloudflare_token"))
	email := strings.TrimSpace(r.FormValue("acme_email"))
	domain := lib.CleanHost(r.FormValue("domain"))
	mode := strings.TrimSpace(r.FormValue("mode"))
	serverIP := strings.TrimSpace(r.FormValue("server_ip"))
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	traefikHost := lib.CleanHost(r.FormValue("traefik_host"))
	managerHost := lib.CleanHost(r.FormValue("manager_host"))
	if domain == "" {
		domain = lib.Env("TCM_DEFAULT_DOMAIN", "")
	}
	if serverIP == "" {
		serverIP = lib.Env("TCM_PUBLIC_IP", "")
	}
	if mode != "internal" && mode != "external" {
		mode = "internal"
	}
	if mode == "internal" {
		if traefikHost == "" {
			traefikHost = "iproxy." + domain
		}
		if managerHost == "" {
			managerHost = "iproxym." + domain
		}
	} else {
		if traefikHost == "" {
			traefikHost = "proxy." + domain
		}
		if managerHost == "" {
			managerHost = "proxym." + domain
		}
	}
	cfg := &models.Config{CloudflareToken: token, AcmeEmail: email, Domain: domain, Mode: mode, ServerIP: serverIP, TraefikHost: traefikHost, ManagerHost: managerHost, Username: username}
	return cfg, password, validateSetup(cfg, password)
}

func parseRequestForm(r *http.Request) error {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		return r.ParseMultipartForm(10 << 20)
	}
	return r.ParseForm()
}

func validateSetup(cfg *models.Config, password string) error {
	switch {
	case cfg.CloudflareToken == "":
		return errors.New("Cloudflare API token is required")
	case !strings.Contains(cfg.AcmeEmail, "@"):
		return errors.New("ACME email must look like an email address")
	case !lib.ValidHost(cfg.Domain):
		return errors.New("Domain is not valid")
	case !lib.ValidIP(cfg.ServerIP):
		return errors.New("Server IP must be an IPv4 or IPv6 address")
	case cfg.Username == "" || strings.Contains(cfg.Username, ":"):
		return errors.New("Traefik username is required and cannot contain ':'")
	case len(password) < 8:
		return errors.New("Password must be at least 8 characters")
	case !lib.ValidHost(cfg.TraefikHost) || !strings.HasSuffix(cfg.TraefikHost, "."+cfg.Domain):
		return errors.New("Traefik host must be inside the configured domain")
	case !lib.ValidHost(cfg.ManagerHost) || !strings.HasSuffix(cfg.ManagerHost, "."+cfg.Domain):
		return errors.New("Manager host must be inside the configured domain")
	}
	return nil
}

func (a *App) runSetupJob(job *setupJob, cfg *models.Config, password string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	progress := func(msg string) { job.set("running", msg) }
	if err := a.completeSetup(ctx, cfg, password, progress); err != nil {
		job.fail(err.Error())
		return
	}
	redirectURL := "https://" + cfg.ManagerHost + "/"
	job.done("Setup complete. Redirecting to "+cfg.ManagerHost+".", redirectURL)
}

func (a *App) completeSetup(ctx context.Context, cfg *models.Config, password string, progress func(string)) error {
	cf := lib.NewCloudflareClient(cfg.CloudflareToken)
	progress("Checking Cloudflare API token.")
	if err := cf.VerifyToken(ctx); err != nil {
		return errors.New("Cloudflare token check failed: " + err.Error())
	}
	progress("Finding Cloudflare zone.")
	zone, err := cf.FindZone(ctx, cfg.Domain)
	if err != nil {
		return errors.New("Cloudflare zone lookup failed: " + err.Error())
	}
	progress("Hashing shared Traefik and manager password.")
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return errors.New("could not hash password")
	}
	cfg.ZoneID = zone.ID
	cfg.PasswordHash = string(hash)
	cfg.Users = []models.User{{
		Username:     cfg.Username,
		PasswordHash: cfg.PasswordHash,
		CreatedAt:    time.Now().UTC(),
	}}
	cfg.Proxies = []models.ProxyConfig{}
	cfg.UpdatedAt = time.Now().UTC()

	proxied := cfg.Mode == "external" && !lib.IsPrivateIP(cfg.ServerIP)
	progress("Creating or updating Traefik dashboard DNS record.")
	if _, err := cf.EnsureARecord(ctx, cfg.ZoneID, cfg.TraefikHost, cfg.ServerIP, proxied); err != nil {
		return errors.New("could not create Traefik DNS record: " + err.Error())
	}
	progress("Creating or updating manager DNS record.")
	if _, err := cf.EnsureARecord(ctx, cfg.ZoneID, cfg.ManagerHost, cfg.ServerIP, proxied); err != nil {
		return errors.New("could not create manager DNS record: " + err.Error())
	}
	progress("Writing Traefik dynamic configuration.")
	if err := lib.WriteTraefikConfig(a.store, cfg); err != nil {
		return errors.New("could not write Traefik config: " + err.Error())
	}
	progress("Saving manager configuration.")
	if err := lib.SaveConfig(a.store, cfg); err != nil {
		return errors.New("could not save manager config: " + err.Error())
	}
	a.setConfig(cfg)
	progress("Starting Traefik container.")
	if err := a.deployTraefik(ctx, cfg); err != nil {
		cfg.LastDeployError = err.Error()
		_ = lib.SaveConfig(a.store, cfg)
		a.setConfig(cfg)
		return errors.New("could not deploy Traefik: " + err.Error())
	}
	progress("Waiting for a valid HTTPS certificate on " + cfg.ManagerHost + ".")
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := lib.WaitForHTTPSReady(waitCtx, cfg.ManagerHost, 5*time.Second); err != nil {
		return errors.New("HTTPS is not ready yet for " + cfg.ManagerHost + ": " + err.Error())
	}
	return nil
}

func defaultSetupView(err string) setupView {
	domain := lib.Env("TCM_DEFAULT_DOMAIN", "")
	traefikHost := ""
	managerHost := ""
	externalHost := ""
	externalApp := ""
	if domain != "" {
		traefikHost = "iproxy." + domain
		managerHost = "iproxym." + domain
		externalHost = "proxy." + domain
		externalApp = "proxym." + domain
	}
	return setupView{
		Error:        err,
		Domain:       domain,
		ServerIP:     lib.Env("TCM_PUBLIC_IP", ""),
		TraefikHost:  traefikHost,
		ManagerHost:  managerHost,
		ExternalHost: externalHost,
		ExternalApp:  externalApp,
	}
}

type setupJob struct {
	mu          sync.RWMutex
	ID          string   `json:"id"`
	State       string   `json:"state"`
	Message     string   `json:"message"`
	Log         []string `json:"log"`
	RedirectURL string   `json:"redirect_url,omitempty"`
}

func newSetupJob() *setupJob {
	raw := make([]byte, 18)
	_, _ = rand.Read(raw)
	id := base64.RawURLEncoding.EncodeToString(raw)
	return &setupJob{ID: id, State: "running", Message: "Starting setup.", Log: []string{"Starting setup."}}
}

func (j *setupJob) set(state, msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.State = state
	j.Message = msg
	j.Log = append(j.Log, time.Now().Format("15:04:05")+" "+msg)
}

func (j *setupJob) fail(msg string) {
	j.set("error", msg)
}

func (j *setupJob) done(msg, redirect string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.State = "done"
	j.Message = msg
	j.RedirectURL = redirect
	j.Log = append(j.Log, time.Now().Format("15:04:05")+" "+msg)
}

type setupJobView struct {
	ID          string   `json:"id"`
	State       string   `json:"state"`
	Message     string   `json:"message"`
	Log         []string `json:"log"`
	RedirectURL string   `json:"redirect_url,omitempty"`
}

func (j *setupJob) snapshot() setupJobView {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return setupJobView{
		ID:          j.ID,
		State:       j.State,
		Message:     j.Message,
		Log:         append([]string(nil), j.Log...),
		RedirectURL: j.RedirectURL,
	}
}
