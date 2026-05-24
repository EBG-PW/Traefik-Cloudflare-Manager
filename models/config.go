package models

import "time"

type Store struct {
	DataDir       string
	DockerVolume  string
	DockerNetwork string
	DockerSocket  string
	TraefikImage  string
}

type Config struct {
	CloudflareToken string        `json:"cloudflare_token"`
	AcmeEmail       string        `json:"acme_email"`
	Domain          string        `json:"domain"`
	ZoneID          string        `json:"zone_id"`
	Mode            string        `json:"mode"`
	ServerIP        string        `json:"server_ip"`
	TraefikHost     string        `json:"traefik_host"`
	ManagerHost     string        `json:"manager_host"`
	Username        string        `json:"username"`
	PasswordHash    string        `json:"password_hash"`
	Users           []User        `json:"users"`
	Proxies         []ProxyConfig `json:"proxies"`
	LastDeployError string        `json:"last_deploy_error,omitempty"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

type ProxyConfig struct {
	Host             string    `json:"host"`
	Protocol         string    `json:"protocol"`
	IP               string    `json:"ip"`
	Port             int       `json:"port"`
	LoadBalancer     bool      `json:"load_balancer"`
	Strategy         string    `json:"strategy"`
	Sticky           bool      `json:"sticky"`
	Backends         []Backend `json:"backends,omitempty"`
	CloudflareProxy  bool      `json:"cloudflare_proxy"`
	CloudflareRecord string    `json:"cloudflare_record,omitempty"`
	Paused           bool      `json:"paused"`
	Status           string    `json:"status"`
	StatusMessage    string    `json:"status_message,omitempty"`
	SSLReady         bool      `json:"ssl_ready"`
	CertNotBefore    time.Time `json:"cert_not_before,omitempty"`
	CertNotAfter     time.Time `json:"cert_not_after,omitempty"`
	CertIssuer       string    `json:"cert_issuer,omitempty"`
	CreatedBy        string    `json:"created_by,omitempty"`
	LastChecked      time.Time `json:"last_checked,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type User struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
}

type Backend struct {
	Protocol string `json:"protocol"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Weight   int    `json:"weight,omitempty"`
}

type DockerStats struct {
	Available bool    `json:"available"`
	CPU       float64 `json:"cpu_percent"`
	Memory    uint64  `json:"memory_bytes"`
	MemLimit  uint64  `json:"memory_limit_bytes"`
	NetRX     uint64  `json:"network_rx_bytes"`
	NetTX     uint64  `json:"network_tx_bytes"`
	Error     string  `json:"error,omitempty"`
}

type ContainerCommandResult struct {
	Command  []string `json:"command"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
}
