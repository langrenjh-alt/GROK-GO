# API compatibility

GROK-GO implements the common OpenAI and Anthropic request paths used by the
reference gateways. Direct xAI-compatible fields are preserved when possible;
protocol bridges sanitize only fields known to be rejected by the selected
upstream.

| Endpoint | Streaming | Notes |
| --- | --- | --- |
| `GET /v1/models` | No | Enabled models with an available credential type |
| `POST /v1/chat/completions` | Yes | Multimodal input, reasoning, function tools, usage and cache tokens |
| `POST /v1/responses` | Yes | Text/reasoning deltas, output-item lifecycle, function calls and usage |
| `POST /v1/messages` | Yes | Anthropic content blocks, thinking, tool use, cache usage and stop reasons |
| `POST /v1/images/generations` | No | Grok Imagine, URL or base64 response formats, up to four images |
| `POST /v1/images/edits` | No | JSON or multipart input with uploaded or URL references |
| `POST /v1/videos` | No | Async 6/10/12/16/20 second job creation |
| `POST /v1/videos/generations` | No | Alias for video creation |
| `GET /v1/videos/{id}` | No | Sticky account lookup |
| `GET /v1/videos/{id}/content` | No | Signed cached content |

## Routing and caching

Each enabled model declares its upstream model, credential kind, capability,
minimum account tier, and whether the highest available tier is preferred.
GROK-GO derives two tenant-isolated, UUID-shaped identities. The session
affinity key prefers an explicit session header, `prompt_cache_key`, supported
metadata session, or Anthropic cache anchor, and otherwise falls back to a
stable conversation prefix. The upstream prompt-cache key is derived
separately from the normalized static prefix: model, system/instructions,
developer messages, and tools. When no static prefix exists, the first user
input is included to avoid concentrating every request in one routing bucket.

CLI OAuth and Console SSO Responses requests receive the derived upstream key
in `prompt_cache_key` and `X-Grok-Conv-Id`; client-provided raw values cannot
override it. The private Grok SSO Web schema does not receive these fields.
xAI still decides cache hits by exact prompt-prefix matching. The key improves
sticky routing to a cache server; it does not make different prefixes share a
cached response, and a missing key does not by itself disable automatic prompt
caching.

Account affinity is a routing optimization, not a local response cache. Redis
refreshes the affinity TTL only when the stored account still matches, keeping
an active conversation pinned while allowing abandoned bindings to expire.
Cached-input token values reported by the upstream are normalized into usage
metadata and dashboard cache metrics; response bodies are never shared between
downstream API keys.

The 24-hour dashboard response separates cache effectiveness, observability,
and affinity-routing measurements:

| Field | Definition |
| --- | --- |
| `cache_token_reuse_rate` | Normalized cached input tokens divided by input tokens for successful conversation requests with parsed usage and positive input |
| `cache_request_hit_rate` | Requests with positive cached tokens divided by those same valid cache samples |
| `cache_usage_coverage` | Successful conversation requests with parsed usage divided by all successful conversation requests |
| `cache_warmup_candidates_24h` | Valid zero-cache samples that established a new account-affinity binding |
| `cache_affinity_reuses_24h` | Valid samples that reused an existing account-affinity binding |
| `cache_affinity_misses_24h` | Affinity reuses that still reported zero cached tokens |
| `cache_affinity_miss_rate` | Affinity misses divided by affinity reuses |

Conversation requests are `/v1/chat/completions`, `/v1/responses`, and
`/v1/messages`. Failed requests and image/video endpoints are excluded from all
cache denominators. Cached tokens are clamped to the range from zero through
input tokens. `cache_hit_rate` remains an alias of
`cache_token_reuse_rate` for existing dashboard clients. Hourly responses use
the corresponding `hourly_cache_*` fields.

An upstream account may be retried only before any downstream response bytes
are committed. Once a streamed response starts, errors are encoded in the
selected protocol and the account lease is released without replaying prior
deltas.

Errors returned before an Anthropic Messages stream use Anthropic's top-level
`type: error` envelope and error types. OpenAI-compatible routes retain their
`error.code` response shape. Errors that occur after SSE headers are committed
remain stream events, but are recorded as failed outcomes in request logs.

## Media behavior

Grok SSO image requests use the Imagine WebSocket route. Image edits upload
data-URI references concurrently, create a media post, and then invoke the
Grok conversation stream. Remote and inline result assets enter the bounded
local cache before a downstream URL is returned. Remote fetches enforce public
address, redirect, type, and size checks.

Video jobs run asynchronously on the originally selected account. Generation
uses one or more Grok segments, exposes progress through the status endpoint,
and caches the completed asset for the content endpoint. Job-to-account and
media bindings are durable or reconstructable across the supported polling
flow. Each job records a stable instance owner; a restarted owner marks its
interrupted in-memory jobs failed because segmented Grok generation cannot be
replayed without duplicating work.

## Management compatibility

The administrator API retains the grok2api-style `/tokens/add`, `/build-oauth`,
and `/tokens` aliases. Accounts can also be imported as JSON, plain text, or
multipart files. These routes use the same administrator session, origin, and
CSRF checks as the native console endpoints.

### Account import and export

`POST /admin/api/accounts/import`, `/admin/api/tokens`, and
`/admin/api/tokens/add` accept the same import parser. Multipart requests may
contain multiple files; JSON requests may contain one supported envelope,
account array, token array, or token-pool object. Import responses report
`imported`, `skipped`, and `failed` counts. Re-importing the same upstream
credential is reported as skipped. Credential fingerprints are keyed and used
only for duplicate detection; credentials remain AES-256-GCM encrypted.

Import controls may be supplied as JSON fields, multipart fields, or query
parameters. `initial_status` (with `status` as an alias) explicitly overrides
the imported account state; `active` also clears an imported cooldown. The
optional `post_import_action` accepts `none` (the API default), `refresh`, or
`refresh_probe`. Post-import actions run only for newly created CLI OAuth
accounts, use bounded concurrency and a request-wide timeout, and return a
`post_action` summary with one result per processed account. An explicitly
disabled account is preserved unless an initial status override activates it.

For a Build OAuth multipart upload, the console sends the following controls by
default. The account remains outside normal scheduling until both refresh and
probe complete successfully:

```bash
curl -X POST 'https://HOST/admin/api/accounts/import' \
  -H 'X-CSRF-Token: CSRF_TOKEN' \
  -F 'initial_status=active' \
  -F 'post_import_action=refresh_probe' \
  -F 'files=@xai-ACCOUNT.json;type=application/json'
```

API clients that need the historical import-only behavior can send
`post_import_action=none`. The same controls can be query parameters when the
request body is one raw Build OAuth JSON object. A single existing account can
be rechecked with `POST /admin/api/accounts/{id}/probe`; the batch endpoint is
`POST /admin/api/accounts/probe` with `{ "ids": ["ACCOUNT_ID"] }`.

Supported interchange forms are:

| Producer | Accepted form | Notes |
| --- | --- | --- |
| GROK-GO | `type: "grok-go-accounts"`, version 1 | Restores credential kind, tier, status, proxy, model/tag filters, scheduling, quota and cooldown fields |
| sub2api | `sub2api-data` or `sub2api-bundle`, version 1 or legacy untagged bundle | Imports only Grok/xAI OAuth accounts; a `{ "data": ... }` wrapper is accepted |
| grok2api | `basic`, `super`, `heavy`, `auto`, `ssoBasic`, `ssoSuper`, `ssoHeavy`, or `{tokens:[...]}` from `GET /tokens` | Values may be strings or objects; per-item `pool`, `status`, `tags`, and `note` are retained where representable |
| CPA / CLIProxyAPI | xAI `auth_kind: "oauth"` JSON or a legacy minimal OAuth object | Access and refresh tokens are required; Unix and RFC 3339 expiry forms are accepted; unknown future metadata is ignored |

Export selected accounts with:

```http
POST /admin/api/accounts/export
Content-Type: application/json
X-CSRF-Token: CSRF_TOKEN

{
  "format": "native",
  "ids": ["ACCOUNT_ID"],
  "current_password": "CURRENT_PASSWORD",
  "totp_code": "TOTP_CODE_WHEN_ENABLED"
}
```

The format is `native`, `sub2api`, `grok2api`, or `cpa`; between one and 500
IDs are required. Current-password verification is always required and TOTP is
also required when enabled. Responses set `Cache-Control: private, no-store`,
`Pragma: no-cache`, `X-Content-Type-Options: nosniff`, and an attachment
filename. These attachments contain plaintext upstream credentials.

- Native export supports all GROK-GO credential kinds and preserves account
  metadata for a version 1 round trip.
- sub2api export is limited to CLI OAuth accounts and includes `extra.grok_go`
  metadata so GROK-GO priority and scheduling values round trip without losing
  sub2api's inverse priority ordering. OAuth expiry remains in
  `credentials.expires_at`; the separate sub2api account-level `expires_at` is
  omitted because GROK-GO does not expose an equivalent administrative expiry.
- grok2api export is limited to Grok SSO accounts and writes the standard
  basic/super/heavy token-pool object.
- CPA export is limited to CLI OAuth. One account produces a CPA JSON file;
  multiple accounts produce a ZIP containing one JSON file per account. Extract
  that archive before selecting its JSON files for a later multipart import.

Account exports do not include proxy definitions or proxy credentials. A
native `proxy_id`, or one retained in sub2api `extra.grok_go`, round trips only
when the target instance already has that proxy ID. Create the corresponding
proxy first, or clear/reassign the account's proxy reference before import.

OAuth import and manual account management pin credential origins. The two
accepted xAI API origins are normalized to the CLI proxy route, Bearer is the
only accepted token type, and imported token endpoints, redirect URIs, and
headers do not override transport configuration. Additional CPA metadata is
accepted for forward compatibility but is not copied into runtime transport.

The first release focuses on Grok reverse-proxy operations. Stored Responses
retrieval, Realtime, audio, embeddings, batches, and account registration are
outside the v0.1 API surface.
