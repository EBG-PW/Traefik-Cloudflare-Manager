package api

import (
	"net/http"
	"net/url"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	cfg := a.currentConfig()
	stats := a.docker.TraefikStats(r.Context())
	pageError := r.URL.Query().Get("err")
	if _, err := a.jsonStore.LoadConfig(); err != nil {
		pageError = "Proxy store is invalid. Traefik configuration will not be overwritten until this is fixed: " + err.Error()
	}
	a.render(w, "dashboard.tmpl", dashboardView{
		Config:       cfg,
		Stats:        stats,
		Message:      r.URL.Query().Get("msg"),
		Error:        pageError,
		LocalWarning: cfg.Mode == "internal" || lib.IsPrivateIP(cfg.ServerIP),
		InsecureHTTP: cfg.Mode == "internal" && !requestIsHTTPS(r),
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
	if err := a.writeCurrentTraefikConfig(); err != nil {
		return err
	}
	if err := a.deployTraefik(r.Context(), cfg); err != nil {
		_, _ = a.jsonStore.UpdateConfig(func(current *models.Config) error {
			current.LastDeployError = err.Error()
			return nil
		})
		return err
	}
	_, _ = a.jsonStore.UpdateConfig(func(current *models.Config) error {
		current.LastDeployError = ""
		return nil
	})
	return nil
}

func (a *App) stopTraefik(r *http.Request) error {
	return a.docker.RemoveContainer(r.Context(), "traefik")
}
