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

## Prompt cache identity

The gateway derives one stable, tenant-isolated UUID-shaped identity from the
strongest available conversation signal: session headers, an explicit prompt
cache key, request metadata, Anthropic ephemeral cache blocks, or the stable
system/tool/first-user prefix. It is used both as the upstream prompt cache
identity and as the account-affinity key. Hashing the downstream credential
into the identity namespace prevents cache affinity from crossing tenants.

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
