package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"traefik-cloudflare-manager/lib"
	"traefik-cloudflare-manager/models"
)

func TestRedeployLifecycle(t *testing.T) {
	for _, test := range []struct {
		name, failure, wantError string
		cancelOnRemove           bool
		wantSteps                []string
	}{
		{name: "disconnect during replacement", cancelOnRemove: true, wantSteps: []string{"mount", "pull", "remove", "create", "start"}},
		{name: "mount failure preserves container", failure: "mount", wantError: "resolve data mount", wantSteps: []string{"mount"}},
		{name: "pull stream error preserves container", failure: "pull", wantError: "registry unavailable", wantSteps: []string{"mount", "pull"}},
		{name: "remove failure prevents creation", failure: "remove", wantError: "remove traefik", wantSteps: []string{"mount", "pull", "remove"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &models.Store{DataDir: t.TempDir(), DockerNetwork: "test-network", TraefikImage: "traefik:v3"}
			var mu sync.Mutex
			var steps []string
			socket := testDockerSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(r.URL.Path, "/"+lib.DockerAPIVersion)
				step := ""
				switch {
				case strings.HasSuffix(path, "/json"):
					step = "mount"
				case path == "/images/create":
					step = "pull"
				case r.Method == http.MethodDelete:
					step = "remove"
				case path == "/containers/create":
					step = "create"
				case path == "/containers/traefik/start":
					step = "start"
				}
				if step != "" {
					mu.Lock()
					steps = append(steps, step)
					mu.Unlock()
				}
				if test.failure == step && step != "" {
					if step == "pull" {
						_, _ = w.Write([]byte(`{"error":"registry unavailable"}`))
					} else {
						http.Error(w, "Docker failure", http.StatusInternalServerError)
					}
					return
				}
				switch step {
				case "mount":
					_ = json.NewEncoder(w).Encode(map[string]any{"Mounts": []map[string]string{{"Type": "volume", "Name": "shared-data", "Destination": store.DataDir}}})
				case "pull":
					_, _ = w.Write([]byte(`{"status":"Downloaded"}`))
				case "remove":
					if test.cancelOnRemove {
						cancel()
					}
					w.WriteHeader(http.StatusNoContent)
				case "create":
					var body struct {
						HostConfig struct {
							Mounts []struct{ Type, Source, Target string }
						}
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode create payload: %v", err)
					}
					mounts := body.HostConfig.Mounts
					if len(mounts) != 1 || mounts[0].Type != "volume" || mounts[0].Source != "shared-data" || mounts[0].Target != "/app/data" {
						t.Errorf("shared data mount was not preserved: %+v", mounts)
					}
					w.WriteHeader(http.StatusCreated)
				case "start":
					w.WriteHeader(http.StatusNoContent)
				default:
					if path == "/networks/create" {
						w.WriteHeader(http.StatusCreated)
					} else {
						w.WriteHeader(http.StatusOK)
					}
				}
			}))
			store.DockerSocket = socket
			jsonStore, err := lib.OpenJSONStore(store)
			if err != nil {
				t.Fatal(err)
			}
			cfg := &models.Config{Domain: "example.com", LastDeployError: "previous failure"}
			if err := jsonStore.SaveInitialConfig(cfg); err != nil {
				t.Fatal(err)
			}
			app := &App{store: store, jsonStore: jsonStore, docker: lib.NewDockerClient(socket)}
			err = app.redeploy(httptest.NewRequest(http.MethodPost, "/traefik/redeploy", nil).WithContext(ctx))
			if test.wantError == "" && err != nil {
				t.Fatalf("redeploy after disconnect: %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("redeploy error = %v, want %q", err, test.wantError)
			}
			mu.Lock()
			gotSteps := append([]string(nil), steps...)
			mu.Unlock()
			if !reflect.DeepEqual(gotSteps, test.wantSteps) {
				t.Fatalf("Docker steps = %v, want %v", gotSteps, test.wantSteps)
			}
			if got := app.currentConfig().LastDeployError; (test.wantError == "" && got != "") || (test.wantError != "" && !strings.Contains(got, test.wantError)) {
				t.Fatalf("persisted deployment error = %q", got)
			}
		})
	}
}

func testDockerSocket(t *testing.T, handler http.Handler) string {
	t.Helper()
	// Keep the socket path below Unix domain socket length limits on all hosts.
	dir, err := os.MkdirTemp("", "tcm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}
