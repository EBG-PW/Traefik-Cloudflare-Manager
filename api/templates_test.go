package api

import (
	"context"
	"net/http/httptest"
	"os/exec"
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
	app.render(recorder, "dashboard.tmpl", dashboardView{Config: cfg, Zones: []string{"example.net"}})
	body := recorder.Body.String()
	for _, expected := range []string{
		`id="open-proxy-editor"`,
		`id="proxy-editor"`,
		`id="proxy-search"`,
		`id="proxy-zone-filter"`,
		`data-zone="example.net"`,
		`id="traefik-version"`,
		`id="traefik-update-form"`,
		`href="https://app.example.net"`,
		`target="_blank"`,
		`example.net`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("dashboard output is missing %q", expected)
		}
	}
}

func TestRenderUsesRequestNonceWithCachedTemplates(t *testing.T) {
	app := &App{}
	recorder := httptest.NewRecorder()
	app.render(recorder, "login.tmpl", pageView{Title: "Login"})
	csp := recorder.Header().Get("Content-Security-Policy")
	marker := "style-src 'self' 'nonce-"
	start := strings.Index(csp, marker)
	if start < 0 {
		t.Fatalf("CSP nonce is missing: %s", csp)
	}
	start += len(marker)
	end := strings.Index(csp[start:], "'")
	if end <= 0 {
		t.Fatalf("invalid CSP nonce: %s", csp)
	}
	nonce := csp[start : start+end]
	if !strings.Contains(recorder.Body.String(), `nonce="`+nonce+`"`) {
		t.Fatal("cached template did not receive the request nonce")
	}
}

func TestDashboardInlineJavaScriptSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	app := &App{}
	recorder := httptest.NewRecorder()
	app.render(recorder, "dashboard.tmpl", dashboardView{
		Config: &models.Config{
			Domain:      "example.com",
			Mode:        "internal",
			TraefikHost: "iproxy.example.com",
			ManagerHost: "iproxym.example.com",
		},
	})
	body := recorder.Body.String()
	start := strings.LastIndex(body, "<script nonce=")
	if start < 0 {
		t.Fatal("dashboard script is missing")
	}
	start = strings.Index(body[start:], ">") + start + 1
	end := strings.Index(body[start:], "</script>")
	if end < 0 {
		t.Fatal("dashboard script is not closed")
	}
	command := exec.Command(node, "--check", "-")
	command.Stdin = strings.NewReader(body[start : start+end])
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dashboard JavaScript has invalid syntax: %v\n%s", err, output)
	}
}

func TestDashboardEscapesDynamicHTMLValues(t *testing.T) {
	app := &App{}
	recorder := httptest.NewRecorder()
	app.render(recorder, "dashboard.tmpl", dashboardView{
		Config: &models.Config{
			Mode:        "internal",
			TraefikHost: "iproxy.example.com",
			ManagerHost: "iproxym.example.com",
		},
	})
	script := recorder.Body.String()
	for _, expected := range []string{
		"escapeHTML(proxy.created_by || '-')",
		"return `${escapeHTML(target)}${locationSummary}`",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("dashboard does not safely render dynamic value: %s", expected)
		}
	}
}

func TestDashboardInitialRenderDoesNotCallDocker(t *testing.T) {
	app := &App{
		cfg: &models.Config{
			Domain:      "example.com",
			Mode:        "internal",
			ServerIP:    "192.168.1.10",
			TraefikHost: "iproxy.example.com",
			ManagerHost: "iproxym.example.com",
			Users:       []models.User{{Username: "admin"}},
		},
		// docker intentionally remains nil: initial rendering must use the
		// asynchronous stats WebSocket instead of a blocking Docker call.
	}
	request := httptest.NewRequest("GET", "http://manager.local/dashboard", nil)
	request = request.WithContext(context.WithValue(request.Context(), authenticatedUserContextKey{}, "admin"))
	recorder := httptest.NewRecorder()
	app.handleDashboard(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("unexpected status %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `id="traefik-not-running">checking</span>`) {
		t.Fatal("dashboard does not show the asynchronous initial state")
	}
}

func TestProxyZonesAreUniqueAndSorted(t *testing.T) {
	zones := proxyZones([]models.ProxyConfig{
		{ZoneName: "z.example"},
		{ZoneName: "a.example"},
		{ZoneName: "z.example"},
		{},
	})
	if strings.Join(zones, ",") != "a.example,z.example" {
		t.Fatalf("unexpected zones: %#v", zones)
	}
}

func TestDashboardTemplateShowsTraefikVersionAndUpdate(t *testing.T) {
	app := &App{}
	recorder := httptest.NewRecorder()
	app.render(recorder, "dashboard.tmpl", dashboardView{
		Config:  &models.Config{Domain: "example.com", Mode: "internal"},
		Stats:   models.DockerStats{Available: true},
		Traefik: models.TraefikVersionInfo{Version: "3.6.24", UpdateAvailable: true},
	})
	body := recorder.Body.String()
	if !strings.Contains(body, `id="traefik-version">3.6.24</span>`) {
		t.Fatal("Traefik version is missing")
	}
	if strings.Contains(body, `id="traefik-update-form" class="d-none"`) {
		t.Fatal("update action is hidden despite an available update")
	}
}

func TestTraefikTemplateUsesLiveWebSocketLogs(t *testing.T) {
	app := &App{}
	recorder := httptest.NewRecorder()
	app.render(recorder, "traefik.tmpl", traefikView{Config: &models.Config{TraefikHost: "proxy.example.com"}})
	body := recorder.Body.String()
	for _, expected := range []string{
		`/api/traefik/logs/ws?tail=`,
		`new WebSocket`,
		`id="log-stream-state"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("Traefik output is missing %q", expected)
		}
	}
}
