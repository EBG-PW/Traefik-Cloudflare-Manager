package lib

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"traefik-cloudflare-manager/models"
)

type DockerClient struct {
	socket string
	http   *http.Client
}

func (d *DockerClient) ContainerLogs(ctx context.Context, name string, tail int) (string, error) {
	tailValue := strconv.Itoa(tail)
	if tail < 0 {
		tailValue = "all"
	} else if tail == 0 || tail > 5000 {
		tail = 500
		tailValue = "500"
	}
	path := fmt.Sprintf("/containers/%s/logs?stdout=1&stderr=1&tail=%s&timestamps=1", url.PathEscape(name), url.QueryEscape(tailValue))
	raw, err := d.dockerRaw(ctx, http.MethodGet, path, nil, http.StatusOK)
	if err != nil {
		return "", err
	}
	stdout, stderr := demuxDockerStream(raw)
	if stderr != "" {
		if stdout != "" {
			return stdout + "\n" + stderr, nil
		}
		return stderr, nil
	}
	return stdout, nil
}

func (d *DockerClient) StreamContainerLogs(ctx context.Context, name string, tail int, consume func(stream, text string) error) error {
	tailValue := strconv.Itoa(tail)
	if tail <= 0 || tail > 5000 {
		tailValue = "500"
	}
	path := fmt.Sprintf("/containers/%s/logs?stdout=1&stderr=1&follow=1&tail=%s&timestamps=1", url.PathEscape(name), url.QueryEscape(tailValue))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/"+DockerAPIVersion+path, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: d.http.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("docker logs returned %s: %s", resp.Status, string(raw))
	}
	err = parseDockerLogStream(resp.Body, consume)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func parseDockerLogStream(reader io.Reader, consume func(stream, text string) error) error {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(reader, header); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("truncated Docker log frame header")
			}
			return err
		}
		size := int(binary.BigEndian.Uint32(header[4:8]))
		if size < 0 || size > 4<<20 {
			return fmt.Errorf("invalid Docker log frame size %d", size)
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return err
		}
		stream := "stdout"
		if header[0] == 2 {
			stream = "stderr"
		} else if header[0] != 1 {
			return fmt.Errorf("unsupported Docker log stream %d", header[0])
		}
		if err := consume(stream, string(payload)); err != nil {
			return err
		}
	}
}

func (d *DockerClient) ExecContainer(ctx context.Context, name string, command []string) (models.ContainerCommandResult, error) {
	if len(command) == 0 {
		return models.ContainerCommandResult{}, fmt.Errorf("command is required")
	}
	createPayload := map[string]any{
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
		"Cmd":          command,
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := d.dockerJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/exec", createPayload, &created, http.StatusCreated); err != nil {
		return models.ContainerCommandResult{}, err
	}
	startPayload := map[string]any{"Detach": false, "Tty": false}
	raw, err := d.dockerRaw(ctx, http.MethodPost, "/exec/"+url.PathEscape(created.ID)+"/start", startPayload, http.StatusOK)
	if err != nil {
		return models.ContainerCommandResult{}, err
	}
	stdout, stderr := demuxDockerStream(raw)
	var inspected struct {
		ExitCode int `json:"ExitCode"`
	}
	if err := d.dockerJSON(ctx, http.MethodGet, "/exec/"+url.PathEscape(created.ID)+"/json", nil, &inspected, http.StatusOK); err != nil {
		return models.ContainerCommandResult{}, err
	}
	return models.ContainerCommandResult{Command: command, ExitCode: inspected.ExitCode, Stdout: stdout, Stderr: stderr}, nil
}

func NewDockerClient(socket string) *DockerClient {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	return &DockerClient{socket: socket, http: &http.Client{Transport: tr, Timeout: 60 * time.Second}}
}

func (d *DockerClient) Ping(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker socket unavailable at %s: %w", d.socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker ping returned %s", resp.Status)
	}
	return nil
}

func (d *DockerClient) EnsureNetwork(ctx context.Context, name string) error {
	payload := map[string]any{"Name": name, "CheckDuplicate": true}
	return d.dockerJSON(ctx, http.MethodPost, "/networks/create", payload, nil, http.StatusCreated, http.StatusOK)
}

func (d *DockerClient) EnsureVolume(ctx context.Context, name string) error {
	payload := map[string]any{"Name": name}
	return d.dockerJSON(ctx, http.MethodPost, "/volumes/create", payload, nil, http.StatusCreated, http.StatusOK)
}

func (d *DockerClient) ConnectNetwork(ctx context.Context, network, container string, aliases ...string) error {
	payload := map[string]any{"Container": container}
	if len(aliases) > 0 {
		payload["EndpointConfig"] = map[string]any{"Aliases": aliases}
	}
	return d.dockerJSON(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/connect", payload, nil, http.StatusOK)
}

func (d *DockerClient) EnsureSelfNetwork(ctx context.Context, network string, aliases ...string) error {
	network = strings.TrimSpace(network)
	if network == "" {
		return errors.New("Docker network is required")
	}
	if err := d.EnsureNetwork(ctx, network); err != nil {
		return err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("resolve manager container ID: %w", err)
	}
	if hostname == "" {
		return errors.New("resolve manager container ID: empty hostname")
	}
	var inspected struct {
		NetworkSettings struct {
			Networks map[string]struct {
				Aliases []string `json:"Aliases"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := d.dockerJSON(ctx, http.MethodGet, "/containers/"+url.PathEscape(hostname)+"/json", nil, &inspected, http.StatusOK); err != nil {
		return err
	}
	endpoint, connected := inspected.NetworkSettings.Networks[network]
	if connected && containsAll(endpoint.Aliases, aliases) {
		return nil
	}
	if connected {
		payload := map[string]any{"Container": hostname, "Force": true}
		if err := d.dockerJSON(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/disconnect", payload, nil, http.StatusOK); err != nil {
			return err
		}
	}
	return d.ConnectNetwork(ctx, network, hostname, aliases...)
}

func containsAll(values, wanted []string) bool {
	present := make(map[string]bool, len(values))
	for _, value := range values {
		present[value] = true
	}
	for _, value := range wanted {
		if !present[value] {
			return false
		}
	}
	return true
}

func (d *DockerClient) PullImage(ctx context.Context, image string) error {
	name, tag := splitImage(image)
	path := "/images/create?fromImage=" + url.QueryEscape(name)
	if tag != "" {
		path += "&tag=" + url.QueryEscape(tag)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/"+DockerAPIVersion+path, nil)
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pull %s returned %s", image, resp.Status)
	}
	return nil
}

func (d *DockerClient) RemoveContainer(ctx context.Context, name string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, "http://docker/"+DockerAPIVersion+"/containers/"+url.PathEscape(name)+"?force=true", nil)
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return fmt.Errorf("remove traefik returned %s: %s", resp.Status, string(raw))
}

func (d *DockerClient) CreateTraefik(ctx context.Context, cfg *models.Config, store *models.Store) error {
	acmeResolvers := strings.TrimSpace(store.ACMEDNSResolvers)
	if acmeResolvers == "" {
		acmeResolvers = "1.1.1.1:53,8.8.8.8:53"
	}
	acmeDelay := strings.TrimSpace(store.ACMEDNSDelay)
	if acmeDelay == "" {
		acmeDelay = "5s"
	}
	cmd := []string{
		"--api.dashboard=true",
		"--log.level=INFO",
		"--providers.file.directory=/app/data/traefik/config",
		"--providers.file.watch=true",
		"--entrypoints.web.address=:80",
		"--entrypoints.web.http.redirections.entrypoint.to=websecure",
		"--entrypoints.web.http.redirections.entrypoint.scheme=https",
		"--entrypoints.websecure.address=:443",
		"--certificatesresolvers.cloudflare.acme.dnschallenge=true",
		"--certificatesresolvers.cloudflare.acme.dnschallenge.provider=cloudflare",
		"--certificatesresolvers.cloudflare.acme.dnschallenge.resolvers=" + acmeResolvers,
		"--certificatesresolvers.cloudflare.acme.dnschallenge.propagation.delaybeforechecks=" + acmeDelay,
		"--certificatesresolvers.cloudflare.acme.email=" + cfg.AcmeEmail,
		"--certificatesresolvers.cloudflare.acme.storage=/app/data/traefik/acme.json",
	}
	mounts := []map[string]any{}
	binds := []string{}
	if store.DockerVolume != "" {
		mounts = append(mounts, map[string]any{"Type": "volume", "Source": store.DockerVolume, "Target": "/app/data"})
	} else {
		dataMount, err := d.ResolveSelfDataMount(ctx, store.DataDir)
		if err != nil {
			return fmt.Errorf("resolve data mount: %w", err)
		}
		switch dataMount.Type {
		case "volume":
			mounts = append(mounts, map[string]any{"Type": "volume", "Source": dataMount.Source, "Target": "/app/data"})
		case "bind":
			binds = append(binds, dataMount.Source+":/app/data")
		default:
			abs, err := filepath.Abs(store.DataDir)
			if err != nil {
				return err
			}
			binds = append(binds, abs+":/app/data")
		}
	}
	hostConfig := map[string]any{
		"Binds":  binds,
		"Mounts": mounts,
		"PortBindings": map[string]any{
			"80/tcp":  []map[string]string{{"HostPort": "80"}},
			"443/tcp": []map[string]string{{"HostPort": "443"}},
		},
		"RestartPolicy": map[string]string{"Name": "unless-stopped"},
	}
	if store.TraefikNoNewPrivileges {
		hostConfig["SecurityOpt"] = []string{"no-new-privileges:true"}
	}
	payload := map[string]any{
		"Image": store.TraefikImage,
		"Env": []string{
			"CF_DNS_API_TOKEN=" + cfg.CloudflareToken,
			"CF_ZONE_API_TOKEN=" + cfg.CloudflareToken,
			"CLOUDFLARE_PROPAGATION_TIMEOUT=" + defaultString(store.ACMEDNSPropagationTTL, "300"),
		},
		"Cmd": cmd,
		"ExposedPorts": map[string]any{
			"80/tcp":  map[string]any{},
			"443/tcp": map[string]any{},
		},
		"HostConfig": hostConfig,
		"NetworkingConfig": map[string]any{
			"EndpointsConfig": map[string]any{store.DockerNetwork: map[string]any{}},
		},
	}
	if err := d.dockerJSON(ctx, http.MethodPost, "/containers/create?name=traefik", payload, nil, http.StatusCreated); err != nil {
		return err
	}
	return d.dockerJSON(ctx, http.MethodPost, "/containers/traefik/start", nil, nil, http.StatusNoContent)
}

func defaultString(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

type DataMount struct {
	Type   string
	Source string
}

func (d *DockerClient) ResolveSelfDataMount(ctx context.Context, dataDir string) (DataMount, error) {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		return DataMount{}, err
	}
	var out struct {
		Mounts []struct {
			Type        string `json:"Type"`
			Name        string `json:"Name"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
	}
	if err := d.dockerJSON(ctx, http.MethodGet, "/containers/"+url.PathEscape(hostname)+"/json", nil, &out, http.StatusOK); err != nil {
		return DataMount{}, err
	}
	target, err := filepath.Abs(dataDir)
	if err != nil {
		return DataMount{}, err
	}
	target = filepath.ToSlash(filepath.Clean(target))
	for _, mount := range out.Mounts {
		destination := filepath.ToSlash(filepath.Clean(mount.Destination))
		if destination != target {
			continue
		}
		if mount.Type == "volume" && mount.Name != "" {
			return DataMount{Type: "volume", Source: mount.Name}, nil
		}
		if mount.Type == "bind" && mount.Source != "" {
			return DataMount{Type: "bind", Source: mount.Source}, nil
		}
	}
	return DataMount{}, fmt.Errorf("no Docker mount found for %s; set TCM_DOCKER_VOLUME or run with a bind/volume mounted at that path", target)
}

func (d *DockerClient) TraefikStats(ctx context.Context) models.DockerStats {
	var raw struct {
		PreCPU   cpuStats `json:"precpu_stats"`
		CPU      cpuStats `json:"cpu_stats"`
		Memory   memStats `json:"memory_stats"`
		Networks map[string]struct {
			RX uint64 `json:"rx_bytes"`
			TX uint64 `json:"tx_bytes"`
		} `json:"networks"`
	}
	err := d.dockerJSON(ctx, http.MethodGet, "/containers/traefik/stats?stream=false", nil, &raw, http.StatusOK)
	if err != nil {
		return models.DockerStats{Error: err.Error()}
	}
	var rx, tx uint64
	for _, n := range raw.Networks {
		rx += n.RX
		tx += n.TX
	}
	cpuDelta := float64(raw.CPU.CPUUsage.Total - raw.PreCPU.CPUUsage.Total)
	systemDelta := float64(raw.CPU.System - raw.PreCPU.System)
	online := float64(raw.CPU.OnlineCPUs)
	if online == 0 {
		online = float64(len(raw.CPU.CPUUsage.Percpu))
	}
	var cpu float64
	if systemDelta > 0 && cpuDelta > 0 && online > 0 {
		cpu = (cpuDelta / systemDelta) * online * 100
	}
	return models.DockerStats{Available: true, CPU: math.Round(cpu*10) / 10, Memory: raw.Memory.Usage, MemLimit: raw.Memory.Limit, NetRX: rx, NetTX: tx}
}

func (d *DockerClient) TraefikVersion(ctx context.Context, targetImage string) (models.TraefikVersionInfo, error) {
	var container struct {
		Image  string `json:"Image"`
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
	}
	if err := d.dockerJSON(ctx, http.MethodGet, "/containers/traefik/json", nil, &container, http.StatusOK); err != nil {
		return models.TraefikVersionInfo{}, err
	}
	info := models.TraefikVersionInfo{
		Image:     container.Config.Image,
		CheckedAt: time.Now().UTC(),
	}
	running, err := d.inspectImage(ctx, container.Image)
	if err != nil {
		return info, err
	}
	info.Version = strings.TrimPrefix(strings.TrimSpace(running.Config.Labels["org.opencontainers.image.version"]), "v")

	targetImage = strings.TrimSpace(targetImage)
	if targetImage == "" {
		targetImage = container.Config.Image
	}
	tagged, taggedErr := d.inspectImage(ctx, targetImage)

	var remote struct {
		Descriptor struct {
			Digest string `json:"digest"`
		} `json:"Descriptor"`
	}
	path := "/distribution/" + url.PathEscape(targetImage) + "/json"
	if err := d.dockerJSON(ctx, http.MethodGet, path, nil, &remote, http.StatusOK); err != nil {
		info.CheckError = err.Error()
		info.UpdateAvailable = imageUpdateAvailable(running.ID, tagged.ID, "", "")
		return info, nil
	}
	taggedID := ""
	if taggedErr == nil {
		taggedID = tagged.ID
	}
	info.UpdateAvailable = imageUpdateAvailable(running.ID, taggedID, imageDigest(running.RepoDigests), remote.Descriptor.Digest)
	return info, nil
}

type dockerImageInspect struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
	Config      struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func (d *DockerClient) inspectImage(ctx context.Context, reference string) (dockerImageInspect, error) {
	var inspected dockerImageInspect
	err := d.dockerJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(reference)+"/json", nil, &inspected, http.StatusOK)
	return inspected, err
}

func imageDigest(repoDigests []string) string {
	for _, value := range repoDigests {
		if separator := strings.LastIndex(value, "@"); separator >= 0 && separator+1 < len(value) {
			return value[separator+1:]
		}
	}
	return ""
}

func imageUpdateAvailable(runningID, taggedID, localDigest, remoteDigest string) bool {
	if taggedID != "" && runningID != taggedID {
		return true
	}
	return localDigest != "" && remoteDigest != "" && localDigest != remoteDigest
}

func (d *DockerClient) dockerJSON(ctx context.Context, method, path string, body any, out any, okStatuses ...int) error {
	raw, err := d.dockerRaw(ctx, method, path, body, okStatuses...)
	if err != nil {
		return err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return err
		}
	}
	return nil
}

func (d *DockerClient) dockerRaw(ctx context.Context, method, path string, body any, okStatuses ...int) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(ctx, method, "http://docker/"+DockerAPIVersion+path, reader)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	ok := false
	for _, status := range okStatuses {
		if resp.StatusCode == status {
			ok = true
			break
		}
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if !ok {
		if resp.StatusCode == http.StatusConflict && strings.Contains(string(raw), "already exists") {
			return raw, nil
		}
		return nil, fmt.Errorf("docker %s %s returned %s: %s", method, path, resp.Status, string(raw))
	}
	return raw, nil
}

func demuxDockerStream(raw []byte) (string, string) {
	var stdout, stderr bytes.Buffer
	for len(raw) >= 8 {
		stream := raw[0]
		size := int(binary.BigEndian.Uint32(raw[4:8]))
		if size < 0 || size > len(raw)-8 || (stream != 1 && stream != 2) {
			return string(raw), ""
		}
		payload := raw[8 : 8+size]
		if stream == 2 {
			stderr.Write(payload)
		} else {
			stdout.Write(payload)
		}
		raw = raw[8+size:]
	}
	if len(raw) > 0 {
		stdout.Write(raw)
	}
	return strings.TrimRight(stdout.String(), "\n"), strings.TrimRight(stderr.String(), "\n")
}

type cpuStats struct {
	CPUUsage struct {
		Total  uint64   `json:"total_usage"`
		Percpu []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	System     uint64 `json:"system_cpu_usage"`
	OnlineCPUs uint64 `json:"online_cpus"`
}

type memStats struct {
	Usage uint64 `json:"usage"`
	Limit uint64 `json:"limit"`
}

func splitImage(image string) (string, string) {
	idx := strings.LastIndex(image, ":")
	if idx <= 0 || strings.Contains(image[idx+1:], "/") {
		return image, ""
	}
	return image[:idx], image[idx+1:]
}
