# Changelog

All notable changes to GROK-GO are documented in this file.

## [0.1.2] - 2026-07-14

### Added

- Added server-side account search and filters for status, credential kind,
  tier, and bound or direct proxy, with matching controls in the account-pool
  console.
- Added single-account and atomic batch deletion, including post-commit pool
  eviction and administrator confirmation flows.
- Added import controls for `initial_status`/`status` and
  `post_import_action=none|refresh|refresh_probe`. The console now defaults new
  CLI OAuth imports to bounded credential refresh and health probing before
  they enter scheduling.
- Added dashboard counters for cache warmup candidates, affinity reuses,
  affinity reuse misses, and the affinity reuse miss rate.

### Changed

- Split the tenant-isolated session-affinity identity from the upstream
  prompt-cache identity. Static prompt components now drive the upstream cache
  key, while explicit session signals and the conversation prefix drive account
  affinity.
- Send the generated `prompt_cache_key` and matching `X-Grok-Conv-Id` on both
  CLI OAuth and Console SSO routes. The private Grok SSO Web schema remains
  unchanged.
- Clear precise local and Redis affinity bindings when their account becomes
  unavailable through cooldown, disablement, quota exhaustion, or terminal
  feedback. Capacity-only fallbacks preserve the existing affinity instead of
  replacing it.
- Hardened account-list reloads and post-import actions with cancellation-aware
  coordination, bounded concurrency, detached completion, and sanitized error
  summaries.

### Fixed

- Fixed account-pool refresh, OAuth refresh, enable/disable, multi-file import,
  batch editing, and selection-aware bulk actions in the administration
  console.
- Fixed Cloudflare challenge responses being exposed as raw HTML or treated as
  permanent account disablement; affected accounts now enter a bounded
  cooldown with a concise diagnostic.

## [0.1.1] - 2026-07-14

### Added

- Added native GROK-GO, sub2api, grok2api, and CPA/CLIProxyAPI account backup
  import compatibility.
- Added sensitive account export at `POST /admin/api/accounts/export`, with
  current-password verification, conditional TOTP verification, no-store
  download headers, and native/sub2api/grok2api/CPA output formats.
- Added multi-file multipart import and per-item imported, skipped, and failed
  results.
- Added migration 007 with keyed, kind-scoped credential fingerprints for
  idempotent account imports.
- Added migration 008 to distinguish parsed usage from missing usage in request
  logs and cache reporting.
- Added migration 009 to redact URL- or credential-bearing historical account
  errors and cap retained diagnostic text.

### Changed

- Normalized imported CLI OAuth credentials to the supported xAI CLI route and
  ignored imported endpoint, redirect, header, and future metadata overrides
  without rejecting forward-compatible CPA files.
- Account selection now treats exhausted request and token quota as unavailable
  and applies a fallback cooldown when reset metadata is missing. Near-limit
  scoring uses the lower remaining request or token ratio.
- Account runtime feedback now uses field-level persistence, preserves manual
  disable and concurrent scheduling edits, and compares the sealed credential
  revision to ignore pre-refresh feedback even when a stable import fingerprint
  is unchanged.
- Account error persistence now stores bounded, redacted summaries rather than
  raw transport or upstream response text.
- Replaced per-slot Redis account leases with an atomic owner-based sorted-set
  lease, idempotent owner replay, and mixed-TTL-safe expiry; active affinity
  TTLs are refreshed with compare-and-expire. Periodic account reloads no longer
  clear a newer coordinated cooldown; only a persisted explicit activation does.
- Rate-limited last-used persistence and stopped last-used-only updates from
  broadcasting account configuration changes.
- Added one coordinated CLI OAuth refresh-and-retry for upstream 401 responses
  received before downstream response commitment.
- Split dashboard cache reporting into token reuse rate, request hit rate, and
  usage coverage. Cache denominators now include only successful conversation
  requests with valid usage samples; failed, image, and video requests are
  excluded. The existing `cache_hit_rate` field remains a token-reuse alias.
- Hardened release builds by staging a fresh web console, pinning the
  vulnerability scanner and release action, and smoke-testing the final Linux
  binary before publishing it.

### Compatibility notes

- Interchange schemas were reviewed against Wei-Shaw/sub2api v0.1.152,
  langrenjh-alt/grok2api-haochi, and CLIProxyAPI's xAI OAuth token storage.
- sub2api import/export covers Grok/xAI CLI OAuth accounts. sub2api priority
  ordering is converted, and GROK-GO metadata is retained under
  `extra.grok_go` for stable metadata round trips. OAuth expiry stays in the
  credential object and is not mapped to sub2api's separate account expiration
  field.
- grok2api import accepts basic/super/heavy, auto, and SSO pool keys with string
  or object entries, plus `/tokens` list exports with per-item pool and status;
  export covers Grok SSO accounts. GROK-GO status markers in exported tags are
  restored when those pool files are imported back into GROK-GO.
- CPA import/export covers xAI OAuth records with complete access and refresh
  credentials. Multi-account CPA export is a ZIP of individual JSON files.
- Account exports do not include proxy definitions. Native and GROK-GO-enriched
  sub2api backups that retain a `proxy_id` require the same proxy ID on the
  target instance, or the reference must be cleared or reassigned before import.

### Upgrade notes

- Back up PostgreSQL before upgrading. Migrations 007 through 009 are embedded and
  apply automatically at startup; existing accounts remain valid and do not
  require credential re-entry. Migration 008 infers parsed usage only for
  historical rows with non-zero token data.
- Before upgrading, merge legacy accounts that use the same credential kind and
  upstream credential. Migration 007 intentionally does not backfill old rows,
  which lets databases containing legacy duplicates migrate successfully.
  A later edit or OAuth refresh writes the fingerprint and exposes any remaining
  duplicate through the unique constraint.
- Stop all v0.1.0 instances before starting v0.1.1 instances. The Redis account
  lease key layout changed, so mixed versions do not share one distributed
  concurrency count. Old lease keys expire by TTL and Redis does not need to be
  flushed.

## [0.1.0] - 2026-07-14

- Initial GROK-GO release.

[0.1.2]: https://github.com/langrenjh-alt/GROK-GO/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/langrenjh-alt/GROK-GO/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/langrenjh-alt/GROK-GO/releases/tag/v0.1.0
