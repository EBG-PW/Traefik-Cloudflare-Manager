package lib

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"traefik-cloudflare-manager/models"
)

func WriteTraefikConfig(store *models.Store, cfg *models.Config) error {
	configDir := filepath.Join(store.DataDir, "traefik", "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	acmePath := filepath.Join(store.DataDir, "traefik", "acme.json")
	if _, err := os.Stat(acmePath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(acmePath, []byte("{}"), 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		_ = os.Chmod(acmePath, 0o600)
	}

	var b strings.Builder
	b.WriteString("http:\n")
	b.WriteString("  routers:\n")
	writeRouter(&b, "traefik-dashboard", cfg.TraefikHost, "api@internal", "dashboard-auth")
	writeRouter(&b, "manager", cfg.ManagerHost, "manager-service", "dashboard-auth")
	for _, p := range cfg.Proxies {
		if p.Paused {
			continue
		}
		writeRouter(&b, RouterName(p.Host), p.Host, RouterName(p.Host)+"-service", "")
		for i, location := range p.Locations {
			writePathRouter(&b, LocationRouterName(p.Host, i), p.Host, location.Path, LocationRouterName(p.Host, i)+"-service")
		}
	}
	b.WriteString("  middlewares:\n")
	b.WriteString("    dashboard-auth:\n")
	b.WriteString("      basicAuth:\n")
	b.WriteString("        users:\n")
	for _, user := range TraefikUsers(cfg) {
		b.WriteString("          - " + yamlQuote(user.Username+":"+user.PasswordHash) + "\n")
	}
	b.WriteString("  services:\n")
	b.WriteString("    manager-service:\n")
	b.WriteString("      loadBalancer:\n")
	b.WriteString("        servers:\n")
	b.WriteString("          - url: " + yamlQuote(Env("TCM_MANAGER_SERVICE_URL", "http://traefik-cloudflare-manager:8080")) + "\n")
	for _, p := range cfg.Proxies {
		if p.Paused {
			continue
		}
		b.WriteString("    " + RouterName(p.Host) + "-service:\n")
		b.WriteString("      loadBalancer:\n")
		// Traefik 3.0 rejects loadBalancer.strategy in file-provider config.
		// Keep the selected strategy in manager state, but do not emit it until
		// the deployed Traefik version supports the field.
		if p.Sticky || p.Strategy == "sticky" {
			b.WriteString("        sticky:\n")
			b.WriteString("          cookie: {}\n")
		}
		b.WriteString("        servers:\n")
		for _, backend := range ProxyBackends(p) {
			target := fmt.Sprintf("%s://%s:%d", backend.Protocol, backend.IP, backend.Port)
			b.WriteString("          - url: " + yamlQuote(target) + "\n")
			if backend.Weight > 0 {
				b.WriteString(fmt.Sprintf("            weight: %d\n", backend.Weight))
			}
		}
		for i, location := range p.Locations {
			b.WriteString("    " + LocationRouterName(p.Host, i) + "-service:\n")
			b.WriteString("      loadBalancer:\n")
			b.WriteString("        servers:\n")
			target := fmt.Sprintf("%s://%s:%d", location.Protocol, location.IP, location.Port)
			b.WriteString("          - url: " + yamlQuote(target) + "\n")
		}
	}

	path := filepath.Join(configDir, TraefikConfigFile)
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func LoadBalancerStrategy(p models.ProxyConfig) string {
	switch strings.ToLower(strings.TrimSpace(p.Strategy)) {
	case "p2c", "hrw", "leasttime":
		return strings.ToLower(strings.TrimSpace(p.Strategy))
	default:
		return "wrr"
	}
}

func LoadBalancerStrategyLabel(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "p2c":
		return "Power of two choices"
	case "hrw":
		return "Highest random weight"
	case "leasttime":
		return "Least time"
	default:
		return "Weighted round robin"
	}
}

func ProxyBackends(p models.ProxyConfig) []models.Backend {
	if len(p.Backends) > 0 {
		return p.Backends
	}
	return []models.Backend{{Protocol: p.Protocol, IP: p.IP, Port: p.Port}}
}

func TraefikUsers(cfg *models.Config) []models.User {
	if len(cfg.Users) > 0 {
		return cfg.Users
	}
	if cfg.Username != "" && cfg.PasswordHash != "" {
		return []models.User{{Username: cfg.Username, PasswordHash: cfg.PasswordHash}}
	}
	return nil
}

func writeRouter(b *strings.Builder, name, host, service, middleware string) {
	b.WriteString("    " + name + ":\n")
	b.WriteString("      rule: " + yamlQuote("Host(`"+host+"`)") + "\n")
	b.WriteString("      entryPoints:\n")
	b.WriteString("        - websecure\n")
	b.WriteString("      service: " + service + "\n")
	b.WriteString("      tls:\n")
	b.WriteString("        certResolver: cloudflare\n")
	if middleware != "" {
		b.WriteString("      middlewares:\n")
		b.WriteString("        - " + middleware + "\n")
	}
}

func writePathRouter(b *strings.Builder, name, host, path, service string) {
	b.WriteString("    " + name + ":\n")
	b.WriteString("      rule: " + yamlQuote("Host(`"+host+"`) && PathPrefix(`"+path+"`)") + "\n")
	b.WriteString("      entryPoints:\n")
	b.WriteString("        - websecure\n")
	b.WriteString("      service: " + service + "\n")
	b.WriteString("      priority: " + fmt.Sprintf("%d", 1000+len(path)) + "\n")
	b.WriteString("      tls:\n")
	b.WriteString("        certResolver: cloudflare\n")
}

func yamlQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func RouterName(host string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9]+`)
	name := strings.Trim(re.ReplaceAllString(host, "-"), "-")
	if name == "" {
		return "proxy"
	}
	return strings.ToLower(name)
}

func LocationRouterName(host string, index int) string {
	return fmt.Sprintf("%s-location-%d", RouterName(host), index+1)
}
