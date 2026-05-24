package lib

const (
	AppName            = "Traefik Cloudflare Manager"
	DefaultListenAddr  = ":8080"
	DefaultDataDir     = "data"
	DefaultDockerSock  = "/var/run/docker.sock"
	DefaultDockerNet   = "traefik-cloudflare-manager"
	DefaultDockerVol   = ""
	DefaultTraefik     = "traefik:v3.0"
	ConfigFileName     = "config.json"
	TraefikConfigFile  = "dynamic.yml"
	DockerAPIVersion   = "v1.44"
	MaxRequestBodySize = 1 << 20
)
