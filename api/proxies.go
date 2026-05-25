package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

type proxyInput struct {
	Host            string            `json:"host"`
	Protocol        string            `json:"protocol"`
	IP              string            `json:"ip"`
	Port            int               `json:"port"`
	LoadBalancer    bool              `json:"load_balancer"`
	Strategy        string            `json:"strategy"`
	Sticky          bool              `json:"sticky"`
	Backends        []models.Backend  `json:"backends"`
	Locations       []models.Location `json:"locations"`
	CloudflareProxy bool              `json:"cloudflare_proxy"`
}

func (a *App) handleProxyCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectErr(w, r, "Could not read proxy form.")
		return
	}
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	input := proxyInput{
		Host:            r.FormValue("host"),
		Protocol:        r.FormValue("protocol"),
		IP:              r.FormValue("ip"),
		Port:            port,
		LoadBalancer:    r.FormValue("load_balancer") == "on",
		Strategy:        r.FormValue("strategy"),
		Sticky:          r.FormValue("sticky") == "on",
		CloudflareProxy: r.FormValue("cloudflare_proxy") == "on",
	}
	if _, err := a.saveProxy(r.Context(), input, a.currentUsername(r)); err != nil {
		redirectErr(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/dashboard?msg="+url.QueryEscape("Proxy is being created. It will turn green when HTTPS is ready."), http.StatusSeeOther)
}

func (a *App) handleProxyDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectErr(w, r, "Could not read delete request.")
		return
	}
	host := lib.CleanHost(r.FormValue("host"))
	msg, err := a.deleteProxy(r.Context(), host)
	if err != nil {
		redirectErr(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/dashboard?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (a *App) handleAPIProxies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		proxies := a.currentConfig().Proxies
		if proxies == nil {
			proxies = []models.ProxyConfig{}
		}
		for _, proxy := range proxies {
			if proxy.Paused || proxy.Status == "ready" {
				continue
			}
			a.scheduleProxyReadyCheck(proxy.Host)
		}
		writeJSON(w, http.StatusOK, proxies)
	case http.MethodPost:
		var input proxyInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		proxy, err := a.saveProxy(r.Context(), input, a.currentUsername(r))
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, proxy)
	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAPIProxyByHost(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/proxies/"), "/")
	parts := strings.Split(rest, "/")
	host := lib.CleanHost(parts[0])
	if host == "" {
		apiError(w, http.StatusBadRequest, "host is required")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch r.Method {
	case http.MethodDelete:
		msg, err := a.deleteProxy(r.Context(), host)
		if err != nil {
			apiError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "message": msg})
	case http.MethodPut:
		var input proxyInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		proxy, err := a.updateProxy(r.Context(), host, input, a.currentUsername(r))
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, proxy)
	case http.MethodPost:
		switch action {
		case "pause":
			proxy, err := a.setProxyPaused(host, true)
			if err != nil {
				apiError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"proxy": proxy})
		case "resume":
			proxy, err := a.setProxyPaused(host, false)
			if err != nil {
				apiError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"proxy": proxy})
		case "check":
			a.scheduleProxyReadyCheck(host)
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "checking"})
		case "delete":
			msg, err := a.deleteProxy(r.Context(), host)
			if err != nil {
				apiError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "message": msg})
		default:
			apiError(w, http.StatusNotFound, "unknown proxy action")
		}
	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) saveProxy(ctx context.Context, input proxyInput, createdBy string) (models.ProxyConfig, error) {
	return a.upsertProxy(ctx, "", input, createdBy)
}

func (a *App) updateProxy(ctx context.Context, oldHost string, input proxyInput, updatedBy string) (models.ProxyConfig, error) {
	return a.upsertProxy(ctx, oldHost, input, updatedBy)
}

func (a *App) upsertProxy(ctx context.Context, oldHost string, input proxyInput, actor string) (models.ProxyConfig, error) {
	cfg := a.currentConfig()
	host := lib.CleanHost(input.Host)
	backends, err := normalizeBackends(input)
	if err != nil {
		return models.ProxyConfig{}, err
	}
	locations, err := normalizeLocations(input.Locations)
	if err != nil {
		return models.ProxyConfig{}, err
	}
	primary := backends[0]
	strategy, sticky := normalizeLoadBalancerOptions(input)
	if !lib.ValidHost(host) || !strings.HasSuffix(host, "."+cfg.Domain) {
		return models.ProxyConfig{}, errString("Proxy domain must be a host inside " + cfg.Domain + ".")
	}
	if host == cfg.TraefikHost || host == cfg.ManagerHost {
		return models.ProxyConfig{}, errString("Proxy domain cannot be the Traefik or manager host.")
	}
	if input.CloudflareProxy && (lib.IsPrivateIP(cfg.ServerIP) || backendsContainPrivateIP(backends) || locationsContainPrivateIP(locations)) {
		return models.ProxyConfig{}, errString("Cloudflare proxy cannot be enabled for local/private IPs.")
	}
	oldIndex := -1
	conflict := false
	var oldProxy models.ProxyConfig
	for i := range cfg.Proxies {
		if oldHost != "" && cfg.Proxies[i].Host == oldHost {
			oldIndex = i
			oldProxy = cfg.Proxies[i]
		}
		if oldHost != "" && cfg.Proxies[i].Host == host && cfg.Proxies[i].Host != oldHost {
			conflict = true
		}
	}
	if oldHost != "" && oldIndex == -1 {
		return models.ProxyConfig{}, errString("Proxy not found.")
	}
	if conflict {
		return models.ProxyConfig{}, errString("Another proxy already uses " + host + ".")
	}
	recordID, err := lib.NewCloudflareClient(cfg.CloudflareToken).EnsureCNAMERecord(ctx, cfg.ZoneID, host, cfg.TraefikHost, input.CloudflareProxy)
	if err != nil {
		return models.ProxyConfig{}, errString("Cloudflare DNS update failed: " + err.Error())
	}
	now := time.Now().UTC()
	proxy := models.ProxyConfig{
		Host:             host,
		Protocol:         primary.Protocol,
		IP:               primary.IP,
		Port:             primary.Port,
		LoadBalancer:     input.LoadBalancer || len(backends) > 1,
		Strategy:         strategy,
		Sticky:           sticky,
		Backends:         backends,
		Locations:        locations,
		CloudflareProxy:  input.CloudflareProxy,
		CloudflareRecord: recordID,
		Status:           "creating",
		StatusMessage:    "DNS and Traefik config are written. Waiting for HTTPS certificate.",
		CreatedBy:        actor,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	replaced := false
	if oldIndex >= 0 {
		proxy.CreatedAt = oldProxy.CreatedAt
		proxy.CreatedBy = oldProxy.CreatedBy
		proxy.Paused = oldProxy.Paused
		cfg.Proxies[oldIndex] = proxy
		replaced = true
	} else {
		for i := range cfg.Proxies {
			if cfg.Proxies[i].Host != host {
				continue
			}
			proxy.CreatedAt = cfg.Proxies[i].CreatedAt
			proxy.CreatedBy = cfg.Proxies[i].CreatedBy
			proxy.Paused = cfg.Proxies[i].Paused
			cfg.Proxies[i] = proxy
			replaced = true
			break
		}
	}
	if !replaced {
		if oldHost != "" {
			return models.ProxyConfig{}, errString("Proxy not found.")
		}
		cfg.Proxies = append(cfg.Proxies, proxy)
	}
	if oldHost != "" && oldHost != host {
		if err := lib.NewCloudflareClient(cfg.CloudflareToken).DeleteDNSRecord(ctx, cfg.ZoneID, oldProxy.CloudflareRecord, oldProxy.Host); err != nil {
			return models.ProxyConfig{}, errString("Old Cloudflare DNS delete failed: " + err.Error())
		}
	}
	sort.Slice(cfg.Proxies, func(i, j int) bool { return cfg.Proxies[i].Host < cfg.Proxies[j].Host })
	if err := lib.WriteTraefikConfig(a.store, cfg); err != nil {
		return models.ProxyConfig{}, errString("Could not write Traefik config: " + err.Error())
	}
	if err := lib.SaveConfig(a.store, cfg); err != nil {
		return models.ProxyConfig{}, errString("Could not save config: " + err.Error())
	}
	a.setConfig(cfg)
	a.scheduleProxyReadyCheck(host)
	return proxy, nil
}

func normalizeBackends(input proxyInput) ([]models.Backend, error) {
	raw := input.Backends
	if len(raw) == 0 {
		raw = []models.Backend{{Protocol: input.Protocol, IP: input.IP, Port: input.Port}}
	}
	backends := make([]models.Backend, 0, len(raw))
	for i, backend := range raw {
		backend.Protocol = strings.ToLower(strings.TrimSpace(backend.Protocol))
		backend.IP = strings.TrimSpace(backend.IP)
		if backend.Protocol == "" {
			backend.Protocol = "http"
		}
		if backend.Protocol != "http" && backend.Protocol != "https" {
			return nil, errString("Backend " + strconv.Itoa(i+1) + " protocol must be http or https.")
		}
		if !lib.ValidIP(backend.IP) {
			return nil, errString("Backend " + strconv.Itoa(i+1) + " IP must be an IPv4 or IPv6 address.")
		}
		if backend.Port < 1 || backend.Port > 65535 {
			return nil, errString("Backend " + strconv.Itoa(i+1) + " port must be between 1 and 65535.")
		}
		if backend.Weight < 0 {
			backend.Weight = 0
		}
		backends = append(backends, backend)
	}
	return backends, nil
}

func normalizeLocations(raw []models.Location) ([]models.Location, error) {
	locations := make([]models.Location, 0, len(raw))
	seen := map[string]bool{}
	for i, location := range raw {
		location.Path = cleanLocationPath(location.Path)
		location.Protocol = strings.ToLower(strings.TrimSpace(location.Protocol))
		location.IP = strings.TrimSpace(location.IP)
		if location.Path == "" {
			continue
		}
		if location.Path == "/" {
			return nil, errString("Location " + strconv.Itoa(i+1) + " path cannot be '/'. Use the main proxy backend for the root path.")
		}
		if strings.ContainsAny(location.Path, "` \t\r\n") {
			return nil, errString("Location " + strconv.Itoa(i+1) + " path contains invalid characters.")
		}
		if seen[location.Path] {
			return nil, errString("Location path " + location.Path + " is already configured.")
		}
		seen[location.Path] = true
		if location.Protocol == "" {
			location.Protocol = "http"
		}
		if location.Protocol != "http" && location.Protocol != "https" {
			return nil, errString("Location " + strconv.Itoa(i+1) + " protocol must be http or https.")
		}
		if !lib.ValidIP(location.IP) {
			return nil, errString("Location " + strconv.Itoa(i+1) + " IP must be an IPv4 or IPv6 address.")
		}
		if location.Port < 1 || location.Port > 65535 {
			return nil, errString("Location " + strconv.Itoa(i+1) + " port must be between 1 and 65535.")
		}
		locations = append(locations, location)
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i].Path < locations[j].Path })
	return locations, nil
}

func cleanLocationPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

func normalizeLoadBalancerOptions(input proxyInput) (string, bool) {
	strategy := normalizeStrategy(input.Strategy)
	sticky := input.Sticky
	if strings.EqualFold(strings.TrimSpace(input.Strategy), "sticky") {
		strategy = "wrr"
		sticky = true
	}
	return strategy, sticky
}

func normalizeStrategy(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "wrr", "p2c", "hrw", "leasttime":
		return strings.ToLower(strings.TrimSpace(strategy))
	case "round_robin":
		return "wrr"
	default:
		return "wrr"
	}
}

func backendsContainPrivateIP(backends []models.Backend) bool {
	for _, backend := range backends {
		if lib.IsPrivateIP(backend.IP) {
			return true
		}
	}
	return false
}

func locationsContainPrivateIP(locations []models.Location) bool {
	for _, location := range locations {
		if lib.IsPrivateIP(location.IP) {
			return true
		}
	}
	return false
}

func (a *App) deleteProxy(ctx context.Context, host string) (string, error) {
	cfg := a.currentConfig()
	next := cfg.Proxies[:0]
	found := false
	var removed models.ProxyConfig
	for _, p := range cfg.Proxies {
		if p.Host == host {
			found = true
			removed = p
			continue
		}
		next = append(next, p)
	}
	if !found {
		return "", errString("Proxy not found.")
	}
	if err := lib.NewCloudflareClient(cfg.CloudflareToken).DeleteDNSRecord(ctx, cfg.ZoneID, removed.CloudflareRecord, removed.Host); err != nil {
		return "", errString("Cloudflare DNS delete failed: " + err.Error())
	}
	cfg.Proxies = next
	if err := lib.WriteTraefikConfig(a.store, cfg); err != nil {
		return "", errString("Could not write Traefik config: " + err.Error())
	}
	if err := lib.SaveConfig(a.store, cfg); err != nil {
		return "", errString("Could not save config: " + err.Error())
	}
	a.setConfig(cfg)
	msg := "Proxy " + host + " removed. Cloudflare DNS record was deleted."
	log.Printf("proxy removed host=%s dns_record=%s dns_deleted=true", host, removed.CloudflareRecord)
	return msg, nil
}

func (a *App) setProxyPaused(host string, paused bool) (models.ProxyConfig, error) {
	cfg := a.currentConfig()
	for i := range cfg.Proxies {
		if cfg.Proxies[i].Host != host {
			continue
		}
		cfg.Proxies[i].Paused = paused
		cfg.Proxies[i].UpdatedAt = time.Now().UTC()
		if paused {
			cfg.Proxies[i].Status = "paused"
			cfg.Proxies[i].StatusMessage = "Paused in Traefik. DNS record is still present."
		} else {
			cfg.Proxies[i].Status = "creating"
			cfg.Proxies[i].StatusMessage = "Enabled in Traefik. Waiting for HTTPS certificate."
		}
		if err := lib.WriteTraefikConfig(a.store, cfg); err != nil {
			return models.ProxyConfig{}, errString("Could not write Traefik config: " + err.Error())
		}
		if err := lib.SaveConfig(a.store, cfg); err != nil {
			return models.ProxyConfig{}, errString("Could not save config: " + err.Error())
		}
		a.setConfig(cfg)
		proxy := cfg.Proxies[i]
		if !paused {
			a.scheduleProxyReadyCheck(host)
		}
		return proxy, nil
	}
	return models.ProxyConfig{}, errString("Proxy not found.")
}

func (a *App) startProxyReconciler() {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		<-timer.C
		a.reconcileProxiesOnce(context.Background())
		timer.Reset(2 * time.Minute)
	}
}

func (a *App) reconcileProxiesOnce(ctx context.Context) {
	if !a.reconcileMu.TryLock() {
		return
	}
	defer a.reconcileMu.Unlock()

	cfg := a.currentConfig()
	if cfg == nil || cfg.CloudflareToken == "" || cfg.ZoneID == "" {
		return
	}
	cf := lib.NewCloudflareClient(cfg.CloudflareToken)
	changed := false
	now := time.Now().UTC()

	for i := range cfg.Proxies {
		proxy := &cfg.Proxies[i]
		recordID, err := cf.EnsureCNAMERecord(ctx, cfg.ZoneID, proxy.Host, cfg.TraefikHost, proxy.CloudflareProxy)
		if err != nil {
			proxy.Status = "error"
			proxy.StatusMessage = "Cloudflare DNS check failed: " + err.Error()
			proxy.LastChecked = now
			proxy.UpdatedAt = now
			changed = true
			log.Printf("proxy reconcile cloudflare failed host=%s: %v", proxy.Host, err)
			continue
		}
		if recordID != "" && recordID != proxy.CloudflareRecord {
			proxy.CloudflareRecord = recordID
			proxy.UpdatedAt = now
			changed = true
		}
		if proxy.Paused {
			continue
		}

		checkCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		cert, err := lib.CheckHTTPSCertificate(checkCtx, proxy.Host)
		cancel()
		proxy.LastChecked = now
		if err != nil {
			proxy.SSLReady = false
			proxy.Status = "creating"
			proxy.StatusMessage = "HTTPS certificate is still being checked: " + err.Error()
		} else {
			proxy.SSLReady = true
			proxy.Status = "ready"
			proxy.StatusMessage = "HTTPS certificate is ready."
			proxy.CertNotBefore = cert.NotBefore
			proxy.CertNotAfter = cert.NotAfter
			proxy.CertIssuer = cert.Issuer
		}
		proxy.UpdatedAt = now
		changed = true
	}

	if !changed {
		return
	}
	if err := lib.SaveConfig(a.store, cfg); err != nil {
		log.Printf("proxy reconcile save failed: %v", err)
		return
	}
	a.setConfig(cfg)
}

func (a *App) scheduleProxyReadyCheck(host string) {
	a.mu.Lock()
	if a.proxyChecks == nil {
		a.proxyChecks = map[string]bool{}
	}
	if a.proxyChecks[host] {
		a.mu.Unlock()
		return
	}
	a.proxyChecks[host] = true
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.proxyChecks, host)
			a.mu.Unlock()
		}()
		a.waitProxyReady(host)
	}()
}

func (a *App) waitProxyReady(host string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cert, err := lib.WaitForHTTPSCertificate(ctx, host, 5*time.Second)
	if err != nil {
		a.updateProxyStatus(host, "creating", false, "HTTPS certificate is still being checked: "+err.Error(), nil)
		return
	}
	a.updateProxyStatus(host, "ready", true, "HTTPS certificate is ready.", &cert)
}

func (a *App) updateProxyStatus(host, status string, sslReady bool, msg string, cert *lib.TLSCertificateInfo) {
	cfg := a.currentConfig()
	for i := range cfg.Proxies {
		if cfg.Proxies[i].Host != host || cfg.Proxies[i].Paused {
			continue
		}
		cfg.Proxies[i].Status = status
		cfg.Proxies[i].SSLReady = sslReady
		cfg.Proxies[i].StatusMessage = msg
		if cert != nil {
			cfg.Proxies[i].CertNotBefore = cert.NotBefore
			cfg.Proxies[i].CertNotAfter = cert.NotAfter
			cfg.Proxies[i].CertIssuer = cert.Issuer
		}
		cfg.Proxies[i].LastChecked = time.Now().UTC()
		cfg.Proxies[i].UpdatedAt = time.Now().UTC()
		_ = lib.SaveConfig(a.store, cfg)
		a.setConfig(cfg)
		return
	}
}

func redirectErr(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/dashboard?err="+url.QueryEscape(msg), http.StatusSeeOther)
}

type errString string

func (e errString) Error() string { return string(e) }
