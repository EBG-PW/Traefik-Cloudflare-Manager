package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"traefik-cloudflare-manager/models"
)

func TestDashboardTemplateRendersProxyEditorAndLinks(t *testing.T) {
	app := &App{}
	cfg := &models.Config{
		Domain:      "example.com",
		Mode:        "internal",
		TraefikHost: "iproxy.example.com",
		ManagerHost: "iproxym.example.com",
		Proxies: []models.ProxyConfig{{
			Host:          "app.example.net",
			ZoneName:      "example.net",
			Protocol:      "http",
			IP:            "10.0.0.10",
			Port:          8080,
			Status:        "waiting_certificate",
			StatusMessage: "Waiting for certificate.",
		}},
	}
	recorder := httptest.NewRecorder()
	app.render(recorder, "dashboard.tmpl", dashboardView{Config: cfg})
	body := recorder.Body.String()
	for _, expected := range []string{
		`id="open-proxy-editor"`,
		`id="proxy-editor"`,
		`href="https://app.example.net"`,
		`target="_blank"`,
		`example.net`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("dashboard output is missing %q", expected)
		}
	}
}
