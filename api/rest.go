package api

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"traefik-cloudflare-manager/lib"
)

var statsUpgrader = websocket.Upgrader{}

func (a *App) handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg := a.currentConfig()
	safe := map[string]any{
		"acme_email":   cfg.AcmeEmail,
		"domain":       cfg.Domain,
		"mode":         cfg.Mode,
		"server_ip":    cfg.ServerIP,
		"traefik_host": cfg.TraefikHost,
		"manager_host": cfg.ManagerHost,
		"username":     cfg.Username,
		"users":        safeUsers(cfg.Users),
		"updated_at":   cfg.UpdatedAt,
	}
	writeJSON(w, http.StatusOK, safe)
}

func (a *App) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, a.sampleStats(r.Context()))
}

func (a *App) handleAPIStatsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := statsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := conn.WriteJSON(a.sampleStats(r.Context())); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) handleAPIRedeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := a.redeploy(r); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "redeployed"})
}

func (a *App) handleAPIStopTraefik(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := a.stopTraefik(r); err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (a *App) handleAPISetup(w http.ResponseWriter, r *http.Request) {
	apiError(w, http.StatusNotFound, "setup is available through /setup on first run")
}

func cfProxyAllowed(serverIP, backendIP string) bool {
	return !lib.IsPrivateIP(serverIP) && !lib.IsPrivateIP(backendIP)
}
