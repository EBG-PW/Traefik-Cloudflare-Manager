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
		cfg, err := a.jsonStore.LoadConfig()
		if err != nil {
			apiError(w, http.StatusInternalServerError, "proxy store is invalid: "+err.Error())
			return
		}
		proxies := cfg.Proxies
		if proxies == nil {
			proxies = []models.ProxyConfig{}
		}
		for _, proxy := range proxies {
			if proxy.Paused || proxy.SSLReady {
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
			apiError(w, proxyErrorStatus(err), err.Error())
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
			apiError(w, proxyErrorStatus(err), err.Error())
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
	if !lib.ValidHost(host) {
		return models.ProxyConfig{}, errString("Proxy domain is not a valid hostname.")
	}
	if host == cfg.TraefikHost || host == cfg.ManagerHost {
		return models.ProxyConfig{}, errString("Proxy domain cannot be the Traefik or manager host.")
	}
	if cfg.Mode == "internal" {
		input.CloudflareProxy = false
	}
	if input.CloudflareProxy && lib.IsPrivateIP(cfg.ServerIP) {
		return models.ProxyConfig{}, errString("Cloudflare proxy cannot be enabled because the Traefik server IP is private/local.")
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
	cf := lib.NewCloudflareClient(cfg.CloudflareToken)
	zone, err := cf.FindZoneForHost(ctx, host)
	if err != nil {
		return models.ProxyConfig{}, errString("Cloudflare zone lookup failed: " + err.Error())
	}
	now := time.Now().UTC()
	proxy := models.ProxyConfig{
		Host:            host,
		ZoneID:          zone.ID,
		ZoneName:        zone.Name,
		Protocol:        primary.Protocol,
		IP:              primary.IP,
		Port:            primary.Port,
		LoadBalancer:    input.LoadBalancer || len(backends) > 1,
		Strategy:        strategy,
		Sticky:          sticky,
		Backends:        backends,
		Locations:       locations,
		CloudflareProxy: input.CloudflareProxy,
		Status:          "provisioning",
		StatusMessage:   "Saved. Waiting for Cloudflare DNS and Traefik configuration.",
		CreatedBy:       actor,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if oldIndex >= 0 {
		proxy.CreatedAt = oldProxy.CreatedAt
		proxy.CreatedBy = oldProxy.CreatedBy
		proxy.Paused = oldProxy.Paused
	}
	if oldHost == "" {
		proxy, err = a.jsonStore.CreateProxy(proxy)
	} else {
		proxy, err = a.jsonStore.ReplaceProxy(oldHost, proxy)
	}
	if err != nil {
		return models.ProxyConfig{}, errString("Could not save proxy: " + err.Error())
	}
	if err := a.provisionProxy(ctx, host); err != nil {
		return proxy, err
	}
	if oldHost != "" && oldHost != host {
		zoneID := oldProxy.ZoneID
		if zoneID == "" {
			zoneID = cfg.ZoneID
		}
		if err := cf.DeleteDNSRecord(ctx, zoneID, oldProxy.CloudflareRecord, oldProxy.Host); err != nil {
			a.markProxyError(host, "Old Cloudflare DNS delete failed: "+err.Error(), 2*time.Minute)
			return proxy, errString("Old Cloudflare DNS delete failed: " + err.Error())
		}
	}
	a.scheduleProxyReadyCheck(host)
	return a.jsonStore.GetProxy(host)
}

func (a *App) provisionProxy(ctx context.Context, host string) error {
	cfg := a.currentConfig()
	proxy, err := a.jsonStore.GetProxy(host)
	if err != nil {
		return errString("Proxy not found.")
	}
	zoneID := proxy.ZoneID
	if zoneID == "" {
		zoneID = cfg.ZoneID
	}
	recordID, err := lib.NewCloudflareClient(cfg.CloudflareToken).EnsureCNAMERecord(ctx, zoneID, proxy.Host, cfg.TraefikHost, proxy.CloudflareProxy)
	if err != nil {
		a.markProxyError(host, "Cloudflare DNS update failed: "+err.Error(), 2*time.Minute)
		return errString("Cloudflare DNS update failed: " + err.Error())
	}
	_, err = a.jsonStore.UpdateProxy(host, func(current *models.ProxyConfig) error {
		current.CloudflareRecord = recordID
		current.Status = "waiting_certificate"
		current.StatusMessage = "DNS and Traefik configuration are ready. Waiting for certificate."
		current.UpdatedAt = time.Now().UTC()
		current.NextRetry = time.Time{}
		return nil
	})
	if err != nil {
		return errString("Could not update proxy: " + err.Error())
	}
	if err := a.writeCurrentTraefikConfig(); err != nil {
		a.markProxyError(host, "Could not write Traefik config: "+err.Error(), 2*time.Minute)
		return errString("Could not write Traefik config: " + err.Error())
	}
	return nil
}

func (a *App) markProxyError(host, message string, retry time.Duration) {
	_, _ = a.jsonStore.UpdateProxy(host, func(proxy *models.ProxyConfig) error {
		proxy.Status = "error"
		proxy.SSLReady = false
		proxy.StatusMessage = message
		proxy.LastChecked = time.Now().UTC()
		proxy.UpdatedAt = proxy.LastChecked
		proxy.NextRetry = proxy.LastChecked.Add(retry)
		return nil
	})
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

func (a *App) deleteProxy(ctx context.Context, host string) (string, error) {
	removed, err := a.jsonStore.UpdateProxy(host, func(proxy *models.ProxyConfig) error {
		proxy.Status = "deleting"
		proxy.StatusMessage = "Removing DNS and Traefik configuration."
		proxy.UpdatedAt = time.Now().UTC()
		return nil
	})
	if err != nil {
		return "", errString("Proxy not found.")
	}
	if err := a.finishDeleteProxy(ctx, removed); err != nil {
		_, _ = a.jsonStore.UpdateProxy(host, func(proxy *models.ProxyConfig) error {
			proxy.Status = "deleting"
			proxy.StatusMessage = "Delete retry pending: " + err.Error()
			proxy.NextRetry = time.Now().UTC().Add(2 * time.Minute)
			proxy.UpdatedAt = time.Now().UTC()
			return nil
		})
		return "", err
	}
	msg := "Proxy " + host + " removed. Cloudflare DNS record was deleted."
	log.Printf("proxy removed host=%s dns_record=%s dns_deleted=true", host, removed.CloudflareRecord)
	return msg, nil
}

func (a *App) finishDeleteProxy(ctx context.Context, removed models.ProxyConfig) error {
	cfg := a.currentConfig()
	zoneID := removed.ZoneID
	if zoneID == "" {
		zoneID = cfg.ZoneID
	}
	if err := lib.NewCloudflareClient(cfg.CloudflareToken).DeleteDNSRecord(ctx, zoneID, removed.CloudflareRecord, removed.Host); err != nil {
		return errString("Cloudflare DNS delete failed: " + err.Error())
	}
	if err := a.writeCurrentTraefikConfig(); err != nil {
		return errString("Could not write Traefik config: " + err.Error())
	}
	if err := a.jsonStore.DeleteProxy(removed.Host); err != nil {
		return errString("Could not remove proxy file: " + err.Error())
	}
	return nil
}

func (a *App) setProxyPaused(host string, paused bool) (models.ProxyConfig, error) {
	proxy, err := a.jsonStore.UpdateProxy(host, func(proxy *models.ProxyConfig) error {
		proxy.Paused = paused
		proxy.UpdatedAt = time.Now().UTC()
		if paused {
			proxy.Status = "paused"
			proxy.StatusMessage = "Paused in Traefik. DNS record is still present."
		} else {
			proxy.Status = "waiting_certificate"
			proxy.StatusMessage = "Enabled in Traefik. Waiting for HTTPS certificate."
		}
		return nil
	})
	if err != nil {
		return models.ProxyConfig{}, errString("Proxy not found.")
	}
	if err := a.writeCurrentTraefikConfig(); err != nil {
		return models.ProxyConfig{}, errString("Could not write Traefik config: " + err.Error())
	}
	if !paused {
		a.scheduleProxyReadyCheck(host)
	}
	return proxy, nil
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
	now := time.Now().UTC()

	for _, snapshot := range cfg.Proxies {
		proxy := snapshot
		if proxy.Status == "deleting" {
			if proxy.NextRetry.IsZero() || !proxy.NextRetry.After(now) {
				if err := a.finishDeleteProxy(ctx, proxy); err != nil {
					log.Printf("proxy delete reconcile failed host=%s: %v", proxy.Host, err)
				}
			}
			continue
		}
		zoneID := proxy.ZoneID
		if zoneID == "" {
			zoneID = cfg.ZoneID
		}
		recordID, err := cf.EnsureCNAMERecord(ctx, zoneID, proxy.Host, cfg.TraefikHost, proxy.CloudflareProxy)
		if err != nil {
			a.markProxyError(proxy.Host, "Cloudflare DNS check failed: "+err.Error(), 2*time.Minute)
			log.Printf("proxy reconcile cloudflare failed host=%s: %v", proxy.Host, err)
			continue
		}
		if recordID != "" && recordID != proxy.CloudflareRecord {
			_, _ = a.jsonStore.UpdateProxy(proxy.Host, func(current *models.ProxyConfig) error {
				current.CloudflareRecord = recordID
				current.UpdatedAt = now
				return nil
			})
		}
		if proxy.Paused {
			continue
		}

		checkCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		cert, err := a.checkProxyCertificate(checkCtx, proxy.Host)
		cancel()
		proxy.LastChecked = now
		if err != nil {
			status := "waiting_certificate"
			if !proxy.CreatedAt.IsZero() && now.Sub(proxy.CreatedAt) > 10*time.Minute {
				status = "error"
			}
			_, _ = a.jsonStore.UpdateProxy(proxy.Host, func(current *models.ProxyConfig) error {
				current.SSLReady = false
				current.Status = status
				current.StatusMessage = "HTTPS certificate check failed: " + err.Error()
				current.LastChecked = now
				current.UpdatedAt = now
				current.NextRetry = now.Add(2 * time.Minute)
				return nil
			})
		} else {
			status, message := certificateStatus(cert.NotAfter, now)
			_, _ = a.jsonStore.UpdateProxy(proxy.Host, func(current *models.ProxyConfig) error {
				current.SSLReady = true
				current.Status = status
				current.StatusMessage = message
				current.CertNotBefore = cert.NotBefore
				current.CertNotAfter = cert.NotAfter
				current.CertIssuer = cert.Issuer
				current.LastChecked = now
				current.UpdatedAt = now
				current.NextRetry = time.Time{}
				return nil
			})
		}
	}
	if err := a.writeCurrentTraefikConfig(); err != nil {
		log.Printf("proxy reconcile render failed: %v", err)
	}
}

func certificateStatus(notAfter, now time.Time) (string, string) {
	remaining := notAfter.Sub(now)
	switch {
	case remaining <= 0:
		return "error", "HTTPS certificate is expired."
	case remaining <= 7*24*time.Hour:
		return "expiring", "HTTPS certificate expires in less than 7 days."
	case remaining <= 30*24*time.Hour:
		return "renewing", "HTTPS certificate is within Traefik's renewal window."
	default:
		return "ready", "HTTPS certificate is ready."
	}
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
	var cert lib.TLSCertificateInfo
	var err error
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		checkCtx, checkCancel := context.WithTimeout(ctx, 10*time.Second)
		cert, err = a.checkProxyCertificate(checkCtx, host)
		checkCancel()
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			a.updateProxyStatus(host, "error", false, "HTTPS certificate check timed out: "+err.Error(), nil)
			return
		case <-ticker.C:
		}
	}
	a.updateProxyStatus(host, "ready", true, "HTTPS certificate is ready.", &cert)
}

func (a *App) checkProxyCertificate(ctx context.Context, host string) (lib.TLSCertificateInfo, error) {
	_, storedErr := lib.ReadACMECertificate(a.store.DataDir, host)
	address := lib.Env("TCM_TRAEFIK_TLS_ADDR", "traefik:443")
	served, servedErr := lib.CheckHTTPSCertificateAt(ctx, address, host)
	if servedErr == nil {
		return served, nil
	}
	cfg := a.currentConfig()
	if cfg != nil && cfg.Mode == "external" {
		if external, err := lib.CheckHTTPSCertificate(ctx, host); err == nil {
			return external, nil
		}
	}
	if storedErr == nil {
		return lib.TLSCertificateInfo{}, errString("certificate exists in acme.json but Traefik is not serving it: " + servedErr.Error())
	}
	return lib.TLSCertificateInfo{}, errString("certificate is not ready: " + storedErr.Error() + "; Traefik probe: " + servedErr.Error())
}

func (a *App) updateProxyStatus(host, status string, sslReady bool, msg string, cert *lib.TLSCertificateInfo) {
	_, _ = a.jsonStore.UpdateProxy(host, func(proxy *models.ProxyConfig) error {
		if proxy.Paused {
			return nil
		}
		proxy.Status = status
		proxy.SSLReady = sslReady
		proxy.StatusMessage = msg
		if cert != nil {
			proxy.CertNotBefore = cert.NotBefore
			proxy.CertNotAfter = cert.NotAfter
			proxy.CertIssuer = cert.Issuer
			proxy.Status, proxy.StatusMessage = certificateStatus(cert.NotAfter, time.Now().UTC())
		}
		proxy.LastChecked = time.Now().UTC()
		proxy.UpdatedAt = proxy.LastChecked
		proxy.NextRetry = time.Time{}
		return nil
	})
}

func redirectErr(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/dashboard?err="+url.QueryEscape(msg), http.StatusSeeOther)
}

type errString string

func (e errString) Error() string { return string(e) }

func proxyErrorStatus(err error) int {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "zone lookup"), strings.Contains(message, "no cloudflare zone"), strings.Contains(message, "permission"):
		return http.StatusUnprocessableEntity
	case strings.Contains(message, "already exists"), strings.Contains(message, "already uses"), strings.Contains(message, "incompatible"):
		return http.StatusConflict
	case strings.Contains(message, "cloudflare dns"), strings.Contains(message, "traefik config"):
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}
