package api

import "traefik-cloudflare-manager/models"

type setupView struct {
	Error        string
	Domain       string
	ServerIP     string
	TraefikHost  string
	ManagerHost  string
	ExternalHost string
	ExternalApp  string
}

type pageView struct {
	Title string
	Error string
}

type dashboardView struct {
	Config       *models.Config
	Stats        models.DockerStats
	Message      string
	Error        string
	LocalWarning bool
	CurrentUser  string
}

type usersView = dashboardView
