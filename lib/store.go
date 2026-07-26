package lib

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"traefik-cloudflare-manager/models"
)

const proxySchemaVersion = 1

func NormalizeConfig(cfg *models.Config) {
	if cfg == nil {
		return
	}
	if len(cfg.Users) == 0 && cfg.Username != "" && cfg.PasswordHash != "" {
		cfg.Users = []models.User{{
			Username:     cfg.Username,
			PasswordHash: cfg.PasswordHash,
			CreatedAt:    cfg.UpdatedAt,
		}}
	}
	if cfg.Username == "" && len(cfg.Users) > 0 {
		cfg.Username = cfg.Users[0].Username
	}
	if cfg.PasswordHash == "" && len(cfg.Users) > 0 {
		cfg.PasswordHash = cfg.Users[0].PasswordHash
	}
	for i := range cfg.Proxies {
		if cfg.Proxies[i].Strategy == "sticky" {
			cfg.Proxies[i].Sticky = true
			cfg.Proxies[i].Strategy = "wrr"
			continue
		}
		cfg.Proxies[i].Strategy = LoadBalancerStrategy(cfg.Proxies[i])
	}
}

// JSONStore keeps global configuration and independently writable proxy files.
// It is intentionally a single-process store.
type JSONStore struct {
	base       *models.Store
	dataMu     sync.RWMutex
	configMu   sync.Mutex
	locksMu    sync.Mutex
	proxyLocks map[string]*sync.Mutex
}

func OpenJSONStore(base *models.Store) (*JSONStore, error) {
	s := &JSONStore{base: base, proxyLocks: make(map[string]*sync.Mutex)}
	if err := os.MkdirAll(filepath.Join(base.DataDir, ProxyDirectory), 0o700); err != nil {
		return nil, err
	}
	if err := s.migrateLegacyProxies(); err != nil {
		return nil, err
	}
	if _, err := s.LoadConfig(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *JSONStore) LoadConfig() (*models.Config, error) {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	s.configMu.Lock()
	defer s.configMu.Unlock()
	cfg, err := loadGlobalConfig(s.base)
	if err != nil || cfg == nil {
		return cfg, err
	}
	proxies, err := s.listProxiesUnlocked()
	if err != nil {
		return nil, err
	}
	cfg.Proxies = proxies
	NormalizeConfig(cfg)
	return cfg, nil
}

// UpdateConfig serializes global config changes and always reloads current
// proxy state so a stale caller cannot overwrite it.
func (s *JSONStore) UpdateConfig(update func(*models.Config) error) (*models.Config, error) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	s.configMu.Lock()
	defer s.configMu.Unlock()
	cfg, err := loadGlobalConfig(s.base)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errors.New("configuration is not initialized")
	}
	cfg.Proxies, err = s.listProxiesUnlocked()
	if err != nil {
		return nil, err
	}
	if err := update(cfg); err != nil {
		return nil, err
	}
	if err := saveGlobalConfig(s.base, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *JSONStore) SaveInitialConfig(cfg *models.Config) error {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if err := saveGlobalConfig(s.base, cfg); err != nil {
		return err
	}
	for _, proxy := range cfg.Proxies {
		proxy.SchemaVersion = proxySchemaVersion
		if proxy.Revision == 0 {
			proxy.Revision = 1
		}
		if err := writeProxyFile(s.proxyPath(proxy.Host), proxy); err != nil {
			return err
		}
	}
	return nil
}

func (s *JSONStore) ListProxies() ([]models.ProxyConfig, error) {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	return s.listProxiesUnlocked()
}

func (s *JSONStore) listProxiesUnlocked() ([]models.ProxyConfig, error) {
	dir := filepath.Join(s.base.DataDir, ProxyDirectory)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	proxies := make([]models.ProxyConfig, 0, len(entries))
	seen := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".tmp.json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		proxy, err := readProxyFile(path)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy file %s: %w", entry.Name(), err)
		}
		if previous, ok := seen[proxy.Host]; ok {
			return nil, fmt.Errorf("duplicate proxy host %s in %s and %s", proxy.Host, previous, entry.Name())
		}
		seen[proxy.Host] = entry.Name()
		proxies = append(proxies, proxy)
	}
	sort.Slice(proxies, func(i, j int) bool { return proxies[i].Host < proxies[j].Host })
	return proxies, nil
}

func (s *JSONStore) GetProxy(host string) (models.ProxyConfig, error) {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	lock := s.proxyLock(host)
	lock.Lock()
	defer lock.Unlock()
	return s.getProxyUnlocked(host)
}

func (s *JSONStore) getProxyUnlocked(host string) (models.ProxyConfig, error) {
	path, err := s.findProxyPath(host)
	if err != nil {
		return models.ProxyConfig{}, err
	}
	return readProxyFile(path)
}

func (s *JSONStore) CreateProxy(proxy models.ProxyConfig) (models.ProxyConfig, error) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	host := CleanHost(proxy.Host)
	lock := s.proxyLock(host)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.findProxyPath(host); err == nil {
		return models.ProxyConfig{}, fmt.Errorf("proxy %s already exists", host)
	} else if !errors.Is(err, os.ErrNotExist) {
		return models.ProxyConfig{}, err
	}
	proxy.Host = host
	proxy.SchemaVersion = proxySchemaVersion
	proxy.Revision = 1
	if err := writeProxyFile(s.proxyPath(host), proxy); err != nil {
		return models.ProxyConfig{}, err
	}
	return proxy, nil
}

func (s *JSONStore) ReplaceProxy(oldHost string, proxy models.ProxyConfig) (models.ProxyConfig, error) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	oldHost, newHost := CleanHost(oldHost), CleanHost(proxy.Host)
	unlock := s.lockHosts(oldHost, newHost)
	defer unlock()
	current, err := s.getProxyUnlocked(oldHost)
	if err != nil {
		return models.ProxyConfig{}, err
	}
	if oldHost != newHost {
		if _, err := s.findProxyPath(newHost); err == nil {
			return models.ProxyConfig{}, fmt.Errorf("proxy %s already exists", newHost)
		} else if !errors.Is(err, os.ErrNotExist) {
			return models.ProxyConfig{}, err
		}
	}
	proxy.SchemaVersion = proxySchemaVersion
	proxy.Revision = current.Revision + 1
	if err := writeProxyFile(s.proxyPath(newHost), proxy); err != nil {
		return models.ProxyConfig{}, err
	}
	if oldHost != newHost {
		oldPath, err := s.findProxyPath(oldHost)
		if err == nil {
			if err := os.Remove(oldPath); err != nil {
				return models.ProxyConfig{}, err
			}
		}
	}
	return proxy, nil
}

func (s *JSONStore) UpdateProxy(host string, update func(*models.ProxyConfig) error) (models.ProxyConfig, error) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	host = CleanHost(host)
	lock := s.proxyLock(host)
	lock.Lock()
	defer lock.Unlock()
	proxy, err := s.getProxyUnlocked(host)
	if err != nil {
		return models.ProxyConfig{}, err
	}
	if err := update(&proxy); err != nil {
		return models.ProxyConfig{}, err
	}
	if CleanHost(proxy.Host) != host {
		return models.ProxyConfig{}, errors.New("UpdateProxy cannot rename a host")
	}
	proxy.SchemaVersion = proxySchemaVersion
	proxy.Revision++
	if err := writeProxyFile(s.proxyPath(host), proxy); err != nil {
		return models.ProxyConfig{}, err
	}
	return proxy, nil
}

func (s *JSONStore) DeleteProxy(host string) error {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	host = CleanHost(host)
	lock := s.proxyLock(host)
	lock.Lock()
	defer lock.Unlock()
	path, err := s.findProxyPath(host)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func (s *JSONStore) proxyLock(host string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	host = CleanHost(host)
	if s.proxyLocks[host] == nil {
		s.proxyLocks[host] = &sync.Mutex{}
	}
	return s.proxyLocks[host]
}

func (s *JSONStore) lockHosts(hosts ...string) func() {
	unique := make(map[string]bool)
	clean := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = CleanHost(host)
		if host != "" && !unique[host] {
			unique[host] = true
			clean = append(clean, host)
		}
	}
	sort.Strings(clean)
	locks := make([]*sync.Mutex, 0, len(clean))
	for _, host := range clean {
		lock := s.proxyLock(host)
		lock.Lock()
		locks = append(locks, lock)
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}
}

func (s *JSONStore) findProxyPath(host string) (string, error) {
	host = CleanHost(host)
	expected := s.proxyPath(host)
	if proxy, err := readProxyFile(expected); err == nil && proxy.Host == host {
		return expected, nil
	}
	proxiesDir := filepath.Join(s.base.DataDir, ProxyDirectory)
	entries, err := os.ReadDir(proxiesDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(proxiesDir, entry.Name())
		proxy, err := readProxyFile(path)
		if err == nil && proxy.Host == host {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}

func (s *JSONStore) proxyPath(host string) string {
	host = CleanHost(host)
	readable := strings.NewReplacer(".", "-", "_", "-", ":", "-").Replace(host)
	readable = strings.Trim(readable, "-")
	if len(readable) > 160 {
		readable = readable[:160]
	}
	sum := sha256.Sum256([]byte(host))
	return filepath.Join(s.base.DataDir, ProxyDirectory, readable+"--"+hex.EncodeToString(sum[:6])+".json")
}

func readProxyFile(path string) (models.ProxyConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return models.ProxyConfig{}, err
	}
	var proxy models.ProxyConfig
	if err := json.Unmarshal(raw, &proxy); err != nil {
		return models.ProxyConfig{}, err
	}
	if proxy.SchemaVersion != proxySchemaVersion {
		return models.ProxyConfig{}, fmt.Errorf("unsupported schema version %d", proxy.SchemaVersion)
	}
	if !ValidHost(proxy.Host) {
		return models.ProxyConfig{}, fmt.Errorf("invalid host %q", proxy.Host)
	}
	return proxy, nil
}

func writeProxyFile(path string, proxy models.ProxyConfig) error {
	proxy.SchemaVersion = proxySchemaVersion
	raw, err := json.MarshalIndent(proxy, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'), 0o600)
}

func loadGlobalConfig(store *models.Store) (*models.Config, error) {
	path := filepath.Join(store.DataDir, ConfigFileName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg models.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	NormalizeConfig(&cfg)
	return &cfg, nil
}

func saveGlobalConfig(store *models.Store, cfg *models.Config) error {
	cfg.UpdatedAt = time.Now().UTC()
	copy := *cfg
	copy.Proxies = nil
	raw, err := json.MarshalIndent(&copy, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(store.DataDir, ConfigFileName), append(raw, '\n'), 0o600)
}

func atomicWrite(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tmpName, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	ok = true
	return nil
}

func (s *JSONStore) migrateLegacyProxies() error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	cfg, err := loadGlobalConfig(s.base)
	if err != nil || cfg == nil || len(cfg.Proxies) == 0 {
		return err
	}
	backup := filepath.Join(s.base.DataDir, ConfigFileName+".pre-proxy-split.bak")
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		raw, err := os.ReadFile(filepath.Join(s.base.DataDir, ConfigFileName))
		if err != nil {
			return err
		}
		if err := atomicWrite(backup, raw, 0o600); err != nil {
			return err
		}
	}
	created := make([]string, 0, len(cfg.Proxies))
	cleanup := func() {
		for _, createdPath := range created {
			_ = os.Remove(createdPath)
		}
	}
	for _, proxy := range cfg.Proxies {
		proxy.SchemaVersion = proxySchemaVersion
		if proxy.Revision == 0 {
			proxy.Revision = 1
		}
		path := s.proxyPath(proxy.Host)
		if _, err := os.Stat(path); err == nil {
			existing, err := readProxyFile(path)
			if err != nil || existing.Host != proxy.Host {
				cleanup()
				return fmt.Errorf("cannot migrate proxy %s: target exists", proxy.Host)
			}
			continue
		}
		if err := writeProxyFile(path, proxy); err != nil {
			cleanup()
			return err
		}
		if _, err := readProxyFile(path); err != nil {
			_ = os.Remove(path)
			cleanup()
			return err
		}
		created = append(created, path)
	}
	cfg.Proxies = nil
	if err := saveGlobalConfig(s.base, cfg); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Backward-compatible helpers used by older tests and setup code.
func LoadConfig(store *models.Store) (*models.Config, error) {
	s, err := OpenJSONStore(store)
	if err != nil {
		return nil, err
	}
	return s.LoadConfig()
}

func SaveConfig(store *models.Store, cfg *models.Config) error {
	s, err := OpenJSONStore(store)
	if err != nil {
		return err
	}
	_, err = s.UpdateConfig(func(current *models.Config) error {
		proxies := current.Proxies
		*current = *cfg
		current.Proxies = proxies
		return nil
	})
	return err
}
