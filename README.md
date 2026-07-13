# GROK-GO

GROK-GO is a self-hosted Grok gateway with OpenAI and Anthropic compatible
interfaces. It combines a Go control/data plane with an embedded Geist-style
administration console in one Linux executable.

The first release supports CLI OAuth, Console SSO, and grok.com SSO account
pools; named downstream API keys; health-aware scheduling; HTTP/SOCKS5 egress
proxies; text, image, image-edit, and asynchronous video requests; and
usage-aware request accounting. The administration console includes account
probing and batch operations, proxy checks, model routing, media lifecycle
operations, request/audit logs, runtime settings, TOTP, and an API debugger.

## Runtime requirements

- Linux amd64
- PostgreSQL 14 or newer
- Redis 7 or newer
- A writable data directory for cached media

Node.js and pnpm are build-time dependencies only. Container files and images
are intentionally not part of this project.

## Configuration

Copy `.env.example` into your service environment and set these required values:

| Variable | Purpose |
| --- | --- |
| `GROK_GO_DATABASE_URL` | PostgreSQL connection URL |
| `GROK_GO_REDIS_URL` | Redis connection URL |
| `GROK_GO_MASTER_KEY` | Base64-encoded 32-byte credential encryption key |
| `GROK_GO_PUBLIC_URL` | Public origin used for OAuth callbacks and media URLs |
| `GROK_GO_BOOTSTRAP_TOKEN` | One-time token used to create the first administrator |

For multiple instances, set `GROK_GO_INSTANCE_ID` to an identifier that is
unique among concurrently running replicas and stable across restarts. If it is
omitted, GROK-GO derives one from the host name and listen address.

Generate the encryption key with `openssl rand -base64 32`. The bootstrap
endpoint is disabled after the first administrator exists.

## Build

```bash
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
pwsh ./scripts/build-web.ps1
go test ./...
go build -trimpath -o bin/grok-go ./cmd/grok-go
```

On Windows, `make release` or `scripts/build-release.ps1` creates the static
`linux/amd64` artifact in `bin/`.

## First start

```bash
export GROK_GO_DATABASE_URL='postgres://grok_go:secret@127.0.0.1:5432/grok_go?sslmode=disable'
export GROK_GO_REDIS_URL='redis://127.0.0.1:6379/0'
export GROK_GO_MASTER_KEY='BASE64_KEY'
export GROK_GO_PUBLIC_URL='https://grok.example.com'
export GROK_GO_BOOTSTRAP_TOKEN='ONE_TIME_TOKEN'
./grok-go
```

Open the configured public URL, create the first administrator, then add an
upstream account and a named client API key. Client secrets are encrypted for
later copying; only keyed verification digests are used on the request path.

## API

```bash
curl https://grok.example.com/v1/chat/completions \
  -H 'Authorization: Bearer gg_live_...' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-4.5",
    "stream": true,
    "messages": [{"role":"user","content":"Hello"}]
  }'
```

Primary endpoints:

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/messages`
- `POST /v1/images/generations`
- `POST /v1/images/edits`
- `POST /v1/videos` and `POST /v1/videos/generations`
- `GET /v1/videos/{id}` and `GET /v1/videos/{id}/content`

Chat Completions, Responses, and Anthropic Messages share one normalized event
pipeline. It preserves reasoning deltas, function calls, usage, cached-input
tokens, SSE heartbeats, and each protocol's terminal event shape. A stable,
tenant-isolated prompt-cache identity is derived from an explicit session,
prompt cache key, metadata, Anthropic cache anchors, or the stable prompt
prefix. The same identity keeps requests on a compatible upstream account.

Image generation uses Grok Imagine for grok.com SSO accounts. Image editing
uploads references and creates the required Grok media post before streaming
the edit. Video creation is asynchronous: generation remains pinned to the
selected account while clients poll the returned job ID, then the final asset
is cached behind a short-lived signed URL.

See [API compatibility](docs/api-compatibility.md) for the supported surface.
See [Performance baseline](docs/performance.md) for reproducible parallel
gateway benchmarks and measurement scope.

## Account import compatibility

The administration API includes grok2api-compatible token import routes:

```bash
curl https://grok.example.com/admin/api/tokens/add \
  -b admin-cookies.txt \
  -H 'X-CSRF-Token: CSRF_TOKEN' \
  -H 'Content-Type: application/json' \
  -d '{"tokens":["SSO_TOKEN"],"pool":"basic","tags":["imported"]}'
```

JSON and text files can also be uploaded from the Accounts page. Imported
secrets are encrypted before persistence and are never returned by list APIs.
Management aliases use the same administrator session and CSRF protection as
the rest of the console.

The embedded model catalog contains the supported Grok text and media presets,
their credential routes, account-tier requirements, and best-tier preference.
Catalog migrations update managed presets without overwriting administrator
customizations; custom models can be added and removed from the console.

## Operations

- `/healthz` reports process liveness.
- `/readyz` checks PostgreSQL, Redis, migrations, and encryption configuration.
- Runtime settings and account scheduling policy changes are published through
  Redis and reloaded by every instance. Account CRUD, probe feedback, cooldown,
  quota, and credential-state changes use the same propagation path.
  Reconnecting instances reconcile from PostgreSQL before accepting subsequent
  notifications.
- Metadata-only request logs use a bounded asynchronous queue. Administrator
  mutations are recorded separately with actor, action, resource, status,
  request ID, and source address; bodies, cookies, and credentials are omitted.
- The included `deploy/grok-go.service` is a hardened systemd unit template.
- Back up PostgreSQL and the configured media directory together. Redis stores
  reconstructable coordination state and does not replace PostgreSQL backups.

## Development

```bash
go test -race ./...
pnpm --dir web lint
pnpm --dir web test
pnpm --dir web build
```

The frontend is a static Next.js export. It uses same-origin REST endpoints and
does not use Server Actions or Next API routes.

## Security defaults

- Upstream credentials use AES-256-GCM at rest.
- Administrator passwords use Argon2id; TOTP is optional.
- API key authentication uses keyed digests; recoverable copies are encrypted
  with AES-256-GCM and exposed only through the authenticated admin API.
- Request and response bodies are excluded from request logs.
- Remote media fetches reject private and local network targets.
- Cached media is addressed by opaque IDs and expiring signatures.
- Remote media is fetched through public-address validation with redirect,
  content-type, and size checks before it enters the local cache.
- Long SSE responses clear the server's absolute write deadline while retaining
  the upstream request timeout and heartbeat. Interrupted video jobs owned by a
  restarted instance are marked failed instead of remaining indefinitely active.

## Attribution

GROK-GO is an independent implementation. Public behavior from
`langrenjh-alt/grok2api-haochi` and `Wei-Shaw/sub2api` informed the compatibility
surface. Vercel Geist documentation defines the UI design target. See `NOTICE`
for license details.

## License

MIT
