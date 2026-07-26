package api

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

func (a *App) enforceTransport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.currentConfig()
		if cfg == nil || strings.HasPrefix(r.URL.Path, "/setup") || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if requestIsHTTPS(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
			next.ServeHTTP(w, r)
			return
		}
		if cfg.Mode == "internal" && privateClient(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "HTTPS is required for the manager in external mode", http.StatusUpgradeRequired)
	})
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// Forwarded headers are accepted only from the private Docker/LAN side.
	return trustedProxyRemote(r) && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func privateClient(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	if ip == nil {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	return ok && (addr.Unmap().IsPrivate() || addr.IsLoopback())
}

func clientIP(r *http.Request) string {
	if trustedProxyRemote(r) {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			return strings.Trim(forwarded, "[] ")
		}
	}
	return remoteIP(r)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.Trim(strings.TrimSpace(r.RemoteAddr), "[]")
}

func trustedProxyRemote(r *http.Request) bool {
	ip, err := netip.ParseAddr(remoteIP(r))
	if err != nil {
		return false
	}
	raw := strings.TrimSpace(os.Getenv("TCM_TRUSTED_PROXY_CIDRS"))
	if raw == "" {
		raw = "172.16.0.0/12"
	}
	for _, item := range strings.Split(raw, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(item))
		if err == nil && prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}
