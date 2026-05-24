package api

import (
	"net/http"
	"net/url"

	"traefik-cloudflare-manager/lib"
)

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	cfg := a.currentConfig()
	stats := a.docker.TraefikStats(r.Context())
	a.render(w, "dashboard.tmpl", dashboardView{
		Config:       cfg,
		Stats:        stats,
		Message:      r.URL.Query().Get("msg"),
		Error:        r.URL.Query().Get("err"),
		LocalWarning: lib.IsPrivateIP(cfg.ServerIP),
		CurrentUser:  a.currentUsername(r),
	})
}

func (a *App) handleRedeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.redeploy(r); err != nil {
		redirectErr(w, r, "Deploy failed: "+err.Error())
		return
	}
	http.Redirect(w, r, "/dashboard?msg="+url.QueryEscape("Traefik redeployed."), http.StatusSeeOther)
}

func (a *App) handleStopTraefik(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.stopTraefik(r); err != nil {
		redirectErr(w, r, "Stop failed: "+err.Error())
		return
	}
	http.Redirect(w, r, "/dashboard?msg="+url.QueryEscape("Traefik container stopped and deleted."), http.StatusSeeOther)
}

func (a *App) redeploy(r *http.Request) error {
	cfg := a.currentConfig()
	if err := lib.WriteTraefikConfig(a.store, cfg); err != nil {
		return err
	}
	if err := a.deployTraefik(r.Context(), cfg); err != nil {
		cfg.LastDeployError = err.Error()
		_ = lib.SaveConfig(a.store, cfg)
		a.setConfig(cfg)
		return err
	}
	cfg.LastDeployError = ""
	_ = lib.SaveConfig(a.store, cfg)
	a.setConfig(cfg)
	return nil
}

func (a *App) stopTraefik(r *http.Request) error {
	return a.docker.RemoveContainer(r.Context(), "traefik")
}
