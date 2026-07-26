package lib

const (
	AppName            = "Traefik Cloudflare Manager"
	DefaultListenAddr  = ":8080"
	DefaultDataDir     = "data"
	DefaultDockerSock  = "/var/run/docker.sock"
	DefaultDockerNet   = "traefik-cloudflare-manager"
	DefaultDockerVol   = ""
	DefaultTraefik     = "traefik:v3.6"
	ConfigFileName     = "config.json"
	ProxyDirectory     = "proxies"
	TraefikConfigFile  = "dynamic.yml"
	DockerAPIVersion   = "v1.44"
	MaxRequestBodySize = 1 << 20
)
