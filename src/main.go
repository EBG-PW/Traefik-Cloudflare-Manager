package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"traefik-cloudflare-manager/api"
	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/middleware"
	"traefik-cloudflare-manager/models"
)

const distrolessNonrootID = 65532

func main() {
	if len(os.Args) > 1 {
		if len(os.Args) == 2 && os.Args[1] == "init-data-permissions" {
			dataDir := lib.Env("TCM_DATA_DIR", lib.DefaultDataDir)
			if err := initializeDataPermissions(dataDir); err != nil {
				log.Fatalf("initialize data permissions: %v", err)
			}
			log.Printf("initialized %s for uid/gid %d", dataDir, distrolessNonrootID)
			return
		}
		log.Fatal("unknown command")
	}
	addr := lib.Env("TCM_LISTEN_ADDR", lib.DefaultListenAddr)
	store := &models.Store{
		DataDir:                lib.Env("TCM_DATA_DIR", lib.DefaultDataDir),
		DockerVolume:           lib.Env("TCM_DOCKER_VOLUME", lib.DefaultDockerVol),
		DockerNetwork:          lib.Env("TCM_DOCKER_NETWORK", lib.DefaultDockerNet),
		DockerSocket:           lib.Env("TCM_DOCKER_SOCKET", lib.DefaultDockerSock),
		TraefikImage:           lib.Env("TCM_TRAEFIK_IMAGE", lib.DefaultTraefik),
		TraefikNoNewPrivileges: lib.EnvBool("TCM_TRAEFIK_NO_NEW_PRIVILEGES", false),
		ManagerServiceURL:      lib.Env("TCM_MANAGER_SERVICE_URL", lib.ManagerServiceURL("traefik-cloudflare-manager", addr)),
		ACMEDNSResolvers:       lib.Env("TCM_ACME_DNS_RESOLVERS", "1.1.1.1:53,8.8.8.8:53"),
		ACMEDNSDelay:           lib.Env("TCM_ACME_DNS_DELAY", "5s"),
		ACMEDNSPropagationTTL:  lib.Env("TCM_ACME_DNS_PROPAGATION_TIMEOUT", "300"),
	}
	if err := os.MkdirAll(store.DataDir, 0o700); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	jsonStore, err := lib.OpenJSONStore(store)
	if err != nil {
		log.Fatalf("open config store: %v", err)
	}
	cfg, err := jsonStore.LoadConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if cfg != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := lib.NewDockerClient(store.DockerSocket).EnsureSelfNetwork(ctx, store.DockerNetwork, "traefik-cloudflare-manager", "manager")
		cancel()
		if err != nil {
			log.Printf("warning: could not attach manager to Docker network %s: %v", store.DockerNetwork, err)
		}
	}
	app := api.NewAppWithJSONStore(store, jsonStore, cfg)
	handler := middleware.SecurityHeaders(middleware.LimitBody(app.Routes()))
	log.Printf("%s listening on %s", lib.AppName, addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}

func initializeDataPermissions(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(dataDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := os.Lchown(path, distrolessNonrootID, distrolessNonrootID); err != nil {
			return fmt.Errorf("chown %s: %w", path, err)
		}
		switch {
		case entry.IsDir():
			if err := os.Chmod(path, 0o700); err != nil {
				return fmt.Errorf("chmod %s: %w", path, err)
			}
		case entry.Type().IsRegular():
			if err := os.Chmod(path, 0o600); err != nil {
				return fmt.Errorf("chmod %s: %w", path, err)
			}
		}
		return nil
	})
}
