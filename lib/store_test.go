package lib

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"traefik-cloudflare-manager/models"
)

func testModelStore(t *testing.T) *models.Store {
	t.Helper()
	return &models.Store{DataDir: t.TempDir()}
}

func TestJSONStoreMigratesLegacyProxies(t *testing.T) {
	base := testModelStore(t)
	legacy := models.Config{
		Domain: "example.com",
		Users:  []models.User{{Username: "admin", PasswordHash: "hash"}},
		Proxies: []models.ProxyConfig{{
			Host:      "app.example.com",
			Protocol:  "http",
			IP:        "10.0.0.10",
			Port:      8080,
			Status:    "ready",
			CreatedAt: time.Now().UTC(),
		}},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base.DataDir, ConfigFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := OpenJSONStore(base)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Proxies) != 1 || cfg.Proxies[0].Host != "app.example.com" {
		t.Fatalf("unexpected migrated proxies: %#v", cfg.Proxies)
	}
	if cfg.Proxies[0].Revision != 1 || cfg.Proxies[0].SchemaVersion != proxySchemaVersion {
		t.Fatalf("missing proxy metadata: %#v", cfg.Proxies[0])
	}
	if _, err := os.Stat(filepath.Join(base.DataDir, ConfigFileName+".pre-proxy-split.bak")); err != nil {
		t.Fatalf("migration backup missing: %v", err)
	}
	globalRaw, err := os.ReadFile(filepath.Join(base.DataDir, ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	var global map[string]any
	if err := json.Unmarshal(globalRaw, &global); err != nil {
		t.Fatal(err)
	}
	if _, exists := global["proxies"]; exists {
		t.Fatalf("global config still contains proxies: %s", globalRaw)
	}
}

func TestJSONStoreSerializesProxyUpdates(t *testing.T) {
	base := testModelStore(t)
	if err := os.WriteFile(filepath.Join(base.DataDir, ConfigFileName), []byte(`{"domain":"example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenJSONStore(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProxy(models.ProxyConfig{Host: "app.example.com", Protocol: "http", IP: "10.0.0.1", Port: 80}); err != nil {
		t.Fatal(err)
	}

	const updates = 40
	var wg sync.WaitGroup
	wg.Add(updates)
	for i := 0; i < updates; i++ {
		go func() {
			defer wg.Done()
			if _, err := store.UpdateProxy("app.example.com", func(proxy *models.ProxyConfig) error {
				proxy.Port++
				return nil
			}); err != nil {
				t.Errorf("update failed: %v", err)
			}
		}()
	}
	wg.Wait()
	proxy, err := store.GetProxy("app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if proxy.Port != 80+updates {
		t.Fatalf("lost update: got port %d", proxy.Port)
	}
	if proxy.Revision != 1+updates {
		t.Fatalf("unexpected revision %d", proxy.Revision)
	}
}

func TestJSONStoreRejectsCorruptProxyFile(t *testing.T) {
	base := testModelStore(t)
	if err := os.WriteFile(filepath.Join(base.DataDir, ConfigFileName), []byte(`{"domain":"example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	proxyDir := filepath.Join(base.DataDir, ProxyDirectory)
	if err := os.MkdirAll(proxyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proxyDir, "broken.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJSONStore(base); err == nil {
		t.Fatal("expected corrupt proxy file to fail store validation")
	}
}
