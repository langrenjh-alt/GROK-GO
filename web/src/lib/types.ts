export type Locale = "zh" | "en";
export type AccountStatus = "active" | "cooldown" | "expired" | "disabled" | "error";
export type CredentialKind = "cli_oauth" | "console_sso" | "grok_sso";

export interface QuotaSnapshot {
  requests_limit?: number;
  requests_remaining?: number;
  requests_unlimited?: boolean;
  tokens_limit?: number;
  tokens_remaining?: number;
  tokens_unlimited?: boolean;
  reset_at?: string;
  observed_at?: string;
}

export interface Account {
  id: string;
  name: string;
  kind: CredentialKind;
  tier: string;
  status: AccountStatus;
  email?: string;
  credential_expires_at?: string;
  proxy_id?: string;
  models?: string[];
  tags?: string[];
  priority: number;
  concurrency_limit: number;
  health_score: number;
  failure_count: number;
  quota?: QuotaSnapshot;
  cooldown_until?: string;
  last_used_at?: string;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

export type AccountSchedulingStrategy = "affinity" | "priority" | "round_robin";

export interface AccountQuotaMetricSummary {
  state: "known" | "partial" | "unknown" | "unlimited" | "mixed";
  limit: number | null;
  used: number | null;
  remaining: number | null;
  usage_percent: number | null;
  reset_at?: string;
  known_accounts: number;
  unknown_accounts: number;
  unlimited_accounts: number;
  window_count: number;
}

export interface AccountQuotaSummary {
  total_accounts: number;
  available_accounts: number;
  requests: AccountQuotaMetricSummary;
  tokens: AccountQuotaMetricSummary;
}

export interface AccountProbeResult {
  account_id: string;
  success: boolean;
  status_code?: number;
  duration_ms: number;
  model: string;
  message: string;
  completed_at: string;
  account?: Account;
}

export interface AccountProbeBatchResult {
  total: number;
  succeeded: number;
  failed: number;
  items: AccountProbeResult[];
}

export interface ProxyRecord {
  id: string;
  name: string;
  enabled: boolean;
  healthy: boolean;
  last_checked_at?: string;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

export interface ClientKey {
  id: string;
  name: string;
  prefix: string;
  secret_available: boolean;
  enabled: boolean;
  rpm: number;
  concurrency_limit: number;
  daily_request_limit: number;
  monthly_token_limit: number;
  last_used_at?: string;
  expires_at?: string;
  created_at: string;
  updated_at: string;
}

export interface ModelSpec {
  id: string;
  upstream_model: string;
  display_name: string;
  capability: "chat" | "responses" | "messages" | "image" | "image_edit" | "video";
  credential_kinds: CredentialKind[];
  minimum_tier?: string;
  aliases?: string[];
  prefer_best: boolean;
  catalog_managed: boolean;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface RequestLog {
  id: string;
  request_id: string;
  client_key_id?: string;
  account_id?: string;
  model?: string;
  endpoint: string;
  status_code: number;
  duration_ms: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  error_code?: string;
  error_summary?: string;
  metadata?: unknown;
  created_at: string;
}

export interface MediaObject {
  id: string;
  kind: string;
  content_type: string;
  size: number;
  expires_at: string;
  created_at: string;
}

export interface ListResponse<T> {
  items: T[];
  total?: number;
  page?: number;
  page_size?: number;
}

export function normalizeList<T>(value: T[] | ListResponse<T> | undefined): ListResponse<T> {
  if (!value) return { items: [], total: 0 };
  return Array.isArray(value) ? { items: value, total: value.length } : value;
}
