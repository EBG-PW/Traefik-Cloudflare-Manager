# Traefik Cloudflare Manager

A small Go web app that bootstraps Traefik with Cloudflare DNS-01 ACME and then switches into a management dashboard for HTTPS reverse proxies.

The management UI uses the Tabler dashboard theme pinned to `@tabler/core@1.4.0`.

## Run in Docker

```powershell
docker compose up -d --build
```

The runtime image is `gcr.io/distroless/static-debian13:nonroot`. A short-lived
`data-init` service updates existing volume ownership before the non-root manager
starts.

On native Linux, set `DOCKER_GID` to the group ID of the Docker socket so the
non-root manager can access it:

```sh
export DOCKER_GID="$(stat -c '%g' /var/run/docker.sock)"
docker compose up -d --build
```

You can persist the value as `DOCKER_GID=...` in `.env`. Docker Desktop normally
exposes the socket with group ID `0`, which is the Compose default.

Open `http://SERVER-IP:8080`, or `http://SERVER-IP:PORT` when `TCM_HTTP_PORT` is set. The first-run form asks for:

- Cloudflare API token
- ACME email
- domain
- internal or external proxy naming
- Traefik dashboard username and password

Internal mode creates `iproxy.domain.tld` for Traefik and `iproxym.domain.tld` for this app. External mode creates `proxy.domain.tld` and `proxym.domain.tld`. These hostnames are editable on the setup form.

Proxy hosts may belong to any Cloudflare zone accessible with the configured API token. Internal mode always creates DNS-only records and can route to private LAN addresses while still obtaining public certificates through DNS-01.

After setup, Traefik is started as a container on ports `80` and `443`. The dashboard can add routes such as:

```text
app.example.com -> http://10.0.0.10:8080
```

Cloudflare proxying is blocked when the backend IP or Traefik server IP is private/local.

## REST API

All `/api/*` routes require an authenticated manager browser session and a matching CSRF token for state-changing requests. HTTP Basic Auth is not accepted by the manager or REST API.

The separate native Traefik dashboard host retains its own Basic Auth middleware; its authorization header is not forwarded through the manager route.

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

- `DOCKER_GID`: host group ID owning `/var/run/docker.sock`; required on native Linux when that group is not `0`
- `TCM_HTTP_PORT`: host port for the setup/management UI, default `8080`
- `TCM_CONTAINER_PORT`: internal port the manager listens on inside Docker, default `8080`
- `TCM_DOCKER_VOLUME`: optional override for the Docker volume shared by the manager and Traefik; normally auto-detected
- `TCM_DOCKER_NETWORK`: Docker network used by the manager and Traefik, default `traefik-cloudflare-manager`
- `TCM_MANAGER_SERVICE_URL`: optional override for the URL Traefik uses to reach this manager, default `http://traefik-cloudflare-manager:TCM_CONTAINER_PORT`
- `TCM_TRAEFIK_TLS_ADDR`: address used to verify the certificate served by Traefik, default `traefik:443`
- `TCM_ACME_DNS_RESOLVERS`: public recursive resolvers used by Traefik/Lego for DNS-01 discovery and propagation checks, default `1.1.1.1:53,8.8.8.8:53`
- `TCM_ACME_DNS_DELAY`: delay before DNS-01 propagation checks, default `5s`
- `TCM_ACME_DNS_PROPAGATION_TIMEOUT`: Cloudflare TXT propagation timeout in seconds, default `300`
- `TCM_TRUSTED_PROXY_CIDRS`: comma-separated networks allowed to supply forwarded HTTPS/client headers, default `172.16.0.0/12`
- `TCM_TRAEFIK_NO_NEW_PRIVILEGES`: set to `true` to add `no-new-privileges:true` to the Traefik container; default `false`
- `TCM_TRAEFIK_IMAGE`: Traefik update channel, default `traefik:v3`; this follows stable Traefik 3.x minor and patch releases but never upgrades to Traefik 4
- `TCM_DEFAULT_DOMAIN`: optional setup-form prefill
- `TCM_PUBLIC_IP`: optional setup-form prefill

## Data layout

- `data/config.json`: global setup and bcrypt-hashed users
- `data/proxies/*.json`: one atomically updated file per proxy
- `data/traefik/acme.json`: Traefik-managed ACME account and certificates
- `data/traefik/config/dynamic.yml`: manager-generated Traefik configuration

Older installations are migrated automatically. The original combined config is preserved as `config.json.pre-proxy-split.bak`.

The manager joins the Traefik network with stable `traefik-cloudflare-manager`
and `manager` aliases during startup. Rebuilding the manager therefore does not
require a Traefik redeploy to restore the HTTPS manager route.

The dashboard reads the running Traefik version from its OCI image metadata and
checks the configured image tag for a newer registry digest every ten minutes.
When an update is available, the update icon pulls that tag and redeploys
Traefik while preserving `acme.json` and the generated proxy configuration.

Global configuration, users, and proxy records are validated once at startup
and then served from an isolated in-memory snapshot. Atomic store updates write
the JSON file first and update the cache only after the write succeeds. Manual
JSON edits while the manager is running require a restart. Docker statistics
and version information load asynchronously and do not delay page rendering.

The proxy overview includes an instant text search and a Cloudflare-zone
dropdown populated from the zones used by the configured proxies.

For SSH deployment, use `scripts/deploy-remote.ps1`. It requires `plink.exe` and `pscp.exe` from PuTTY when using `.ppk` keys.
