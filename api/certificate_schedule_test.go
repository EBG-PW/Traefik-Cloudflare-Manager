package api

import (
	"context"
	"testing"
	"time"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

func TestCertificateCheckSchedule(t *testing.T) {
	now := time.Now().UTC()
	healthy := models.ProxyConfig{SSLReady: true, LastChecked: now, CertNotAfter: now.Add(90 * 24 * time.Hour)}
	for _, test := range []struct {
		name    string
		proxy   models.ProxyConfig
		elapsed time.Duration
		want    bool
	}{
		{"healthy after two minutes", healthy, 2 * time.Minute, false},
		{"healthy before six hours", healthy, 6*time.Hour - time.Second, false},
		{"healthy at six hours", healthy, 6 * time.Hour, true},
		{"never checked", models.ProxyConfig{SSLReady: true, CertNotAfter: healthy.CertNotAfter}, 0, true},
		{"expiry sooner", models.ProxyConfig{SSLReady: true, LastChecked: now, CertNotAfter: now.Add(time.Hour)}, time.Hour, true},
		{"pending", models.ProxyConfig{}, 0, true},
		{"failed before retry", models.ProxyConfig{NextRetry: now.Add(2 * time.Minute)}, time.Minute, false},
		{"failed at retry", models.ProxyConfig{NextRetry: now.Add(2 * time.Minute)}, 2 * time.Minute, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := proxyCertificateCheckDue(test.proxy, now.Add(test.elapsed)); got != test.want {
				t.Fatalf("check due = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCertificateWarningsUpdateWithoutProbeAfterRestart(t *testing.T) {
	t.Setenv("TCM_TRAEFIK_TLS_ADDR", "127.0.0.1:0")
	now := time.Now().UTC()
	for _, test := range []struct {
		name      string
		remaining time.Duration
		want      string
	}{
		{"renewal window", 29 * 24 * time.Hour, "renewing"},
		{"expiry warning", 6 * 24 * time.Hour, "expiring"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &models.Store{DataDir: t.TempDir()}
			jsonStore, err := lib.OpenJSONStore(store)
			if err != nil {
				t.Fatal(err)
			}
			cfg := &models.Config{CloudflareToken: "unused", ZoneID: "zone", Domain: "example.com",
				TraefikHost: "proxy.example.com", ManagerHost: "manager.example.com",
				Proxies: []models.ProxyConfig{{Host: "app.example.com", Protocol: "http", IP: "10.0.0.1", Port: 80,
					SSLReady: true, Status: "ready", LastChecked: now, CertNotAfter: now.Add(test.remaining), NextDNSCheck: now.Add(time.Hour)}},
			}
			if err := jsonStore.SaveInitialConfig(cfg); err != nil {
				t.Fatal(err)
			}
			jsonStore, err = lib.OpenJSONStore(store)
			if err != nil {
				t.Fatal(err)
			}
			app := &App{store: store, jsonStore: jsonStore}
			app.reconcileProxiesOnce(context.Background())
			proxy, err := jsonStore.GetProxy("app.example.com")
			if err != nil {
				t.Fatal(err)
			}
			// A probe to the closed port would fail and change LastChecked/SSLReady.
			if proxy.Status != test.want || !proxy.SSLReady || !proxy.LastChecked.Equal(now) {
				t.Fatalf("cached certificate status = %+v", proxy)
			}
		})
	}
}
