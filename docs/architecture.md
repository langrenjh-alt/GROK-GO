# Architecture

GROK-GO is a single-process control plane and data plane. The compiled Next.js
application, SQL migrations, and model catalog are embedded in the Go binary.
PostgreSQL is the durable system of record. Redis holds short-lived coordination
state such as sessions, API rate counters, account leases, refresh locks,
cooldowns, cache affinity, video-to-account bindings, and runtime configuration
notifications. Every instance reconciles runtime settings and account policy
from PostgreSQL when its Redis subscription starts or reconnects. Persistent
account mutations and health-state transitions publish a separate account
scope so peers stop scheduling disabled or cooling accounts immediately.

## Request flow

1. Authenticate the downstream API key and apply its request, token, and
   concurrency limits.
2. Resolve the public model to an enabled upstream model and capability.
3. Select a compatible account using health, quota, inflight work, priority,
   and cache affinity.
4. Build an account-specific transport and upstream request.
5. Normalize upstream events, then encode them as OpenAI Chat, Responses, or
   Anthropic events.
6. Release the account lease and persist metadata-only usage and error data.

Retries are limited to failures that happen before response bytes are committed.
This prevents duplicated deltas and tool calls in streamed responses.

Request logging is decoupled from the request path by a bounded worker queue.
When PostgreSQL remains slow long enough to fill that queue, new metadata logs
are counted and dropped instead of back-pressuring gateway traffic.
The upstream HTTP client keeps independent connection pools per proxy in a
bounded LRU, so high-concurrency traffic reuses transports without allowing an
unbounded number of proxy pools. Account batch updates are validated up front
and committed in one PostgreSQL transaction before the in-memory pool reloads.

Distributed concurrency uses one Redis sorted set per account. An atomic Lua
operation removes expired owners and reserves a member only when the configured
limit has capacity. Replaying the same owner is idempotent even at capacity,
and the key expiry follows the latest live member so a short lease cannot
truncate a longer one. The random owner value prevents one request from
releasing another request's lease. Account affinity uses compare-and-expire so
active bindings extend their lifetime only while the value remains unchanged.

Health feedback is persisted through a field-level runtime update. It changes
only status, health, failure, quota, cooldown, last-used, and error fields; it
does not overwrite an administrator's scheduling edits. A manually disabled
account remains disabled, and stale feedback is ignored after the sealed
credential ciphertext changes, including access-token rotations that preserve
the stable import fingerprint. Last-used-only writes are rate-limited and do
not publish configuration notifications.

Both request and token quota participate in account eligibility. When the
provider reports exhaustion without a usable reset timestamp, the pool applies
the configured credential-kind cooldown instead of immediately rescheduling
the account. Permanent credential failures disable the account; rate limits
and transient failures enter bounded cooldown states. Candidate scoring uses
the lower bounded request/token remaining ratio. A success from a request that
started before a concurrent failure cannot clear the newer unexpired cooldown;
terminal disabled, expired, and error states also require an administrative or
credential-refresh transition rather than stale runtime feedback.

## Prompt cache identity

The gateway derives two stable UUID-shaped identities. The session-affinity
identity uses the strongest available conversation signal: session headers,
an explicit prompt cache key, request metadata, Anthropic ephemeral cache
blocks, or the stable system/tool/first-user prefix. It includes a digest of
the downstream credential so account affinity cannot cross tenants.

The upstream prompt-cache identity uses the model and normalized static prompt
prefix and is global across downstream API keys. This lets identical system,
developer, and tool prefixes share upstream cache routing without merging
conversation affinity or sharing response bodies. When no static prefix exists,
the first user input is included so unrelated requests do not collapse into one
cache identity.
The Redis affinity TTL is refreshed on a successful compare against the current
binding. GROK-GO does not cache text response bodies locally; cache-rate values
come from upstream cached-input token usage.

Request logs record whether usage was actually parsed instead of inferring that
fact from a zero token count. Cache aggregates include only successful Chat
Completions, Responses, and Messages requests. Token reuse and request-hit
denominators additionally require parsed usage and positive input tokens;
usage coverage exposes how many otherwise eligible responses supplied parsed
usage. Migration `008_request_log_usage_parsed.sql` marks historical rows with
non-zero token data as parsed and leaves ambiguous zero-token rows unknown.

Account health errors are stored as bounded summaries. Values containing URLs,
authorization material, or token field names are replaced with a generic
redacted marker. Migration `009_redact_account_errors.sql` applies the same
rule to historical account rows created before this behavior existed.

## Account backup identity

Migration `007_account_credential_fingerprints.sql` adds a nullable credential
fingerprint and a unique partial index scoped by credential kind. The
fingerprint is a keyed HMAC derived from the stable upstream identity: the
refresh token when present (otherwise the access token) for OAuth accounts, and
the SSO token for SSO accounts. This makes imports idempotent while keeping
plaintext credentials out of the index. OAuth access-token rotation preserves
identity when the refresh token is unchanged.

Existing rows remain readable because the column is nullable. During import,
legacy rows without a stored fingerprint are compared through decrypted
credentials, and new writes populate the fingerprint. The fingerprint is only
the stable duplicate-detection identity. Runtime feedback separately carries
the observed AES-GCM ciphertext as a credential revision, so a request using an
older access token cannot overwrite state after a concurrent credential
replacement even when the refresh token and import fingerprint are unchanged.

Migration 007 intentionally does not backfill legacy rows: an older database
may already contain duplicate credentials, and backfilling them inside the
migration would prevent startup. Operators should consolidate accounts with the
same credential kind and upstream credential before upgrading. A later account
edit or OAuth refresh writes the fingerprint, at which point the unique index
surfaces any remaining duplicate.

## Media pipelines

Grok Imagine generation uses the grok.com WebSocket protocol. Image editing
uploads inline references in parallel, creates a Grok media post, and consumes
the conversation SSE stream. Video generation is represented as a local
asynchronous job and remains bound to the selected account across all segments.
Returned assets pass through the bounded local cache and signed URL layer. A
stable instance owner distinguishes interrupted jobs from work still running on
another replica; startup marks only that owner's jobs and stale legacy jobs as
failed.

## Model catalog

The embedded Grok catalog separates grok.com SSO chat and media modes, CLI
OAuth, and Console SSO routes. Image generation and image editing use distinct
capabilities so each public endpoint accepts only a compatible preset. Models
with `prefer_best` select the highest available account tier before applying
the configured scheduling strategy.

Catalog rows carry `catalog_managed = true`. An administrator update clears
that flag in the same database write, turning the row into a custom override.
Later catalog migrations update only managed rows. During the legacy upgrade,
only rows that still exactly match the original seed are marked as managed, so
existing custom models and customized presets remain unchanged.

## Trust boundaries

- Upstream credentials are encrypted before PostgreSQL persistence.
- Client API key secrets are encrypted for administrative retrieval; only keyed
  digests are used for request authentication.
- Remote media fetches reject local, private, multicast, and link-local targets.
- Media URLs use expiring signatures and opaque identifiers.
- Request and response bodies are not written to request logs.
- Administrator writes are captured in a separate audit log without request
  bodies, cookies, or credential material.
