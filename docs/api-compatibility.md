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
Requests with an explicit session header, `prompt_cache_key`, supported
metadata session, Anthropic cache anchor, or stable prompt prefix receive a
tenant-isolated UUID-shaped cache identity. That identity is sent to cache-
capable upstream routes and is also used for account affinity.

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

The first release focuses on Grok reverse-proxy operations. Stored Responses
retrieval, Realtime, audio, embeddings, batches, and account registration are
outside the v0.1 API surface.
