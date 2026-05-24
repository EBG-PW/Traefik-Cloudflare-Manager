package main

import (
	"log"
	"net/http"
	"os"

	"traefik-cloudflare-manager/api"
	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/middleware"
	"traefik-cloudflare-manager/models"
)

func main() {
	store := &models.Store{
		DataDir:       lib.Env("TCM_DATA_DIR", lib.DefaultDataDir),
		DockerVolume:  lib.Env("TCM_DOCKER_VOLUME", lib.DefaultDockerVol),
		DockerNetwork: lib.Env("TCM_DOCKER_NETWORK", lib.DefaultDockerNet),
		DockerSocket:  lib.Env("TCM_DOCKER_SOCKET", lib.DefaultDockerSock),
		TraefikImage:  lib.Env("TCM_TRAEFIK_IMAGE", lib.DefaultTraefik),
	}
	if err := os.MkdirAll(store.DataDir, 0o700); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	cfg, err := lib.LoadConfig(store)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	app := api.NewApp(store, cfg)
	handler := middleware.SecurityHeaders(middleware.LimitBody(app.Routes()))
	addr := lib.Env("TCM_LISTEN_ADDR", lib.DefaultListenAddr)
	log.Printf("%s listening on %s", lib.AppName, addr)
	log.Fatal(http.ListenAndServe(addr, handler))
}
