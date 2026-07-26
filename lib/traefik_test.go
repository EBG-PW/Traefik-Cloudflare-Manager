package lib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"traefik-cloudflare-manager/models"
)

func TestManagerRouterDoesNotUseBasicAuth(t *testing.T) {
	store := &models.Store{DataDir: t.TempDir(), ManagerServiceURL: "http://manager:8080"}
	cfg := &models.Config{
		TraefikHost: "traefik.example.com",
		ManagerHost: "manager.example.com",
		Users:       []models.User{{Username: "admin", PasswordHash: "hash"}},
	}
	if err := WriteTraefikConfig(store, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.DataDir, "traefik", "config", TraefikConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	config := string(raw)
	managerStart := strings.Index(config, "    manager:\n")
	middlewareStart := strings.Index(config, "\n  middlewares:\n")
	if managerStart < 0 || middlewareStart <= managerStart {
		t.Fatalf("manager router was not generated:\n%s", config)
	}
	managerRouter := config[managerStart:middlewareStart]
	if strings.Contains(managerRouter, "middlewares:") || strings.Contains(managerRouter, "traefik-dashboard-auth") {
		t.Fatalf("manager router still uses Basic Auth:\n%s", managerRouter)
	}
	if !strings.Contains(config, "    traefik-dashboard-auth:\n") ||
		!strings.Contains(config, "        - traefik-dashboard-auth\n") {
		t.Fatalf("native Traefik dashboard lost its authentication middleware:\n%s", config)
	}
}
