package lib

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"
)

func TestParseDockerLogStream(t *testing.T) {
	var stream bytes.Buffer
	for _, frame := range []struct {
		kind byte
		text string
	}{
		{kind: 1, text: "first line\n"},
		{kind: 2, text: "error line\n"},
	} {
		header := make([]byte, 8)
		header[0] = frame.kind
		binary.BigEndian.PutUint32(header[4:], uint32(len(frame.text)))
		stream.Write(header)
		stream.WriteString(frame.text)
	}
	var received []string
	err := parseDockerLogStream(&stream, func(kind, text string) error {
		received = append(received, kind+":"+text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0] != "stdout:first line\n" || received[1] != "stderr:error line\n" {
		t.Fatalf("unexpected frames: %#v", received)
	}
}

func TestImageDigest(t *testing.T) {
	got := imageDigest([]string{"traefik@sha256:abc123"})
	if got != "sha256:abc123" {
		t.Fatalf("unexpected digest %q", got)
	}
	if got := imageDigest([]string{"invalid"}); got != "" {
		t.Fatalf("expected no digest, got %q", got)
	}
}

func TestImageUpdateAvailable(t *testing.T) {
	tests := []struct {
		name                               string
		runningID, taggedID, local, remote string
		want                               bool
	}{
		{name: "current", runningID: "image-a", taggedID: "image-a", local: "sha256:a", remote: "sha256:a"},
		{name: "new image already pulled", runningID: "image-a", taggedID: "image-b", want: true},
		{name: "remote digest changed", runningID: "image-a", taggedID: "image-a", local: "sha256:a", remote: "sha256:b", want: true},
		{name: "unknown remote", runningID: "image-a", taggedID: "image-a", local: "sha256:a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := imageUpdateAvailable(test.runningID, test.taggedID, test.local, test.remote); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestContainsAll(t *testing.T) {
	if !containsAll([]string{"container-id", "manager", "traefik-cloudflare-manager"}, []string{"manager", "traefik-cloudflare-manager"}) {
		t.Fatal("expected aliases to match")
	}
	if containsAll([]string{"manager"}, []string{"manager", "traefik-cloudflare-manager"}) {
		t.Fatal("missing alias was accepted")
	}
}

func TestDockerNetworkAndTraefikVersionIntegration(t *testing.T) {
	if os.Getenv("TCM_DOCKER_INTEGRATION") != "1" {
		t.Skip("set TCM_DOCKER_INTEGRATION=1 inside a Linux container with the Docker socket mounted")
	}
	network := os.Getenv("TCM_TEST_DOCKER_NETWORK")
	if network == "" {
		t.Fatal("TCM_TEST_DOCKER_NETWORK is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := NewDockerClient(DefaultDockerSock)
	if err := client.EnsureSelfNetwork(ctx, network, "traefik-cloudflare-manager", "manager"); err != nil {
		t.Fatal(err)
	}
	if err := client.EnsureSelfNetwork(ctx, network, "traefik-cloudflare-manager", "manager"); err != nil {
		t.Fatalf("network reconciliation is not idempotent: %v", err)
	}
	info, err := client.TraefikVersion(ctx, "traefik:v3")
	if err != nil {
		t.Fatal(err)
	}
	if info.Version == "" {
		t.Fatal("Traefik image version label was not detected")
	}
	if info.CheckError != "" {
		t.Fatalf("remote update check failed: %s", info.CheckError)
	}
	if os.Getenv("TCM_EXPECT_TRAEFIK_UPDATE") == "1" && !info.UpdateAvailable {
		t.Fatalf("expected an update from running %s to traefik:v3: %#v", info.Version, info)
	}
}
