# Traefik Cloudflare Manager

A small Go web app that bootstraps Traefik with Cloudflare DNS-01 ACME and then switches into a management dashboard for HTTPS reverse proxies.

The management UI uses the Tabler dashboard theme pinned to `@tabler/core@1.4.0`.

## Run in Docker

```powershell
docker compose up -d --build
```

Open `http://SERVER-IP:8080`. The first-run form asks for:

- Cloudflare API token
- ACME email
- domain
- internal or external proxy naming
- Traefik dashboard username and password

Internal mode creates `iproxy.domain.tld` for Traefik and `iproxym.domain.tld` for this app. External mode creates `proxy.domain.tld` and `proxym.domain.tld`. These hostnames are editable on the setup form.

After setup, Traefik is started as a container on ports `80` and `443`. The dashboard can add routes such as:

```text
app.example.com -> http://10.0.0.10:8080
```

Cloudflare proxying is blocked when the backend IP or Traefik server IP is private/local.

## REST API

All `/api/*` routes require the same login credentials as the UI. Browser sessions and HTTP Basic Auth are both accepted.

- `GET /api/config`
- `GET /api/proxies`
- `POST /api/proxies`
- `DELETE /api/proxies/{host}`
- `GET /api/traefik/stats`
- `POST /api/traefik/redeploy`

Example proxy body:

```json
{
  "host": "app.example.com",
  "protocol": "http",
  "ip": "10.0.0.10",
  "port": 8080,
  "cloudflare_proxy": false
}
```

## Configuration

Optional environment variables:

- `TCM_HTTP_PORT`: host port for the setup/management UI, default `8080`
- `TCM_DOCKER_VOLUME`: optional override for the Docker volume shared by the manager and Traefik; normally auto-detected
- `TCM_DOCKER_NETWORK`: Docker network used by the manager and Traefik, default `traefik-cloudflare-manager`
- `TCM_DEFAULT_DOMAIN`: optional setup-form prefill
- `TCM_PUBLIC_IP`: optional setup-form prefill

For SSH deployment, use `scripts/deploy-remote.ps1`. It requires `plink.exe` and `pscp.exe` from PuTTY when using `.ppk` keys.
