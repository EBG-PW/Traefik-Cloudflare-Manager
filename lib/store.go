package lib

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"traefik-cloudflare-manager/models"
)

func LoadConfig(store *models.Store) (*models.Config, error) {
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

func SaveConfig(store *models.Store, cfg *models.Config) error {
	cfg.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(store.DataDir, ConfigFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
