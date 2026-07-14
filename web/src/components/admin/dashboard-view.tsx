"use client";

import Link from "next/link";
import { Activity, ArrowUpRight, Clock3, Database, KeyRound, RefreshCw, ShieldCheck, Users, Zap } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Button, buttonVariants } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ErrorState } from "@/components/ui/feedback";
import { useI18n } from "@/components/i18n-provider";
import { useResource } from "@/lib/use-resource";
import { formatNumber, formatRelative } from "@/lib/format";
import type { RequestLog } from "@/lib/types";
import { ContentFrame } from "./shared";

interface DashboardData {
  requests_24h: number;
  success_rate: number;
  avg_latency_ms: number;
  tokens_24h: number;
  input_tokens_24h: number;
  cached_tokens_24h: number;
  usage_samples_24h: number;
  cache_samples_24h: number;
  cache_request_hits_24h: number;
  cache_warmup_candidates_24h: number;
  cache_affinity_reuses_24h: number;
  cache_affinity_misses_24h: number;
  cache_eligible_requests_24h: number;
  cache_hit_rate: number;
  cache_token_reuse_rate: number;
  cache_request_hit_rate: number;
  cache_usage_coverage: number;
  cache_affinity_miss_rate: number;
  active_accounts: number;
  total_accounts: number;
  active_keys: number;
  gateway_healthy: boolean;
  hourly_requests: number[];
  hourly_input_tokens: number[];
  hourly_cached_tokens: number[];
  hourly_usage_samples: number[];
  hourly_cache_samples: number[];
  hourly_cache_warmup_candidates: number[];
  hourly_cache_affinity_reuses: number[];
  hourly_cache_affinity_misses: number[];
  hourly_cache_hit_rate: number[];
  hourly_cache_token_reuse_rate: number[];
  recent_logs: RequestLog[];
}

const emptyHours = () => Array.from({ length: 24 }, () => 0);

const initialDashboard: DashboardData = {
  requests_24h: 0,
  success_rate: 0,
  avg_latency_ms: 0,
  tokens_24h: 0,
  input_tokens_24h: 0,
  cached_tokens_24h: 0,
  usage_samples_24h: 0,
  cache_samples_24h: 0,
  cache_request_hits_24h: 0,
  cache_warmup_candidates_24h: 0,
  cache_affinity_reuses_24h: 0,
  cache_affinity_misses_24h: 0,
  cache_eligible_requests_24h: 0,
  cache_hit_rate: 0,
  cache_token_reuse_rate: 0,
  cache_request_hit_rate: 0,
  cache_usage_coverage: 0,
  cache_affinity_miss_rate: 0,
  active_accounts: 0,
  total_accounts: 0,
  active_keys: 0,
  gateway_healthy: true,
  hourly_requests: emptyHours(),
  hourly_input_tokens: emptyHours(),
  hourly_cached_tokens: emptyHours(),
  hourly_usage_samples: emptyHours(),
  hourly_cache_samples: emptyHours(),
  hourly_cache_warmup_candidates: emptyHours(),
  hourly_cache_affinity_reuses: emptyHours(),
  hourly_cache_affinity_misses: emptyHours(),
  hourly_cache_hit_rate: emptyHours(),
  hourly_cache_token_reuse_rate: emptyHours(),
  recent_logs: [],
};

function Stat({ label, value, detail, icon: Icon, accent = false, className = "" }: { label: string; value: string; detail: string; icon: typeof Activity; accent?: boolean; className?: string }) {
  return (
    <div className={`min-w-0 border-b border-r border-border bg-surface px-4 py-4 md:border-b-0 sm:px-5 ${className}`}>
      <div className={`flex items-center justify-between gap-3 ${accent ? "text-green" : "text-fg-muted"}`}>
        <span className="text-label-13 font-medium">{label}</span>
        <Icon className="size-3.5 shrink-0" />
      </div>
      <div className="mt-2.5 truncate text-heading-24 font-semibold tabular-nums text-fg">{value}</div>
      <p className="mt-0.5 min-h-5 truncate text-copy-13 text-fg-subtle">{detail}</p>
    </div>
  );
}

function hourSeries(values: number[] | undefined, maximum?: number) {
  return Array.from({ length: 24 }, (_, index) => {
    const value = values?.[index] ?? 0;
    if (!Number.isFinite(value)) return 0;
    return Math.max(0, maximum === undefined ? value : Math.min(maximum, value));
  });
}

function CacheTrend({ rates, inputTokens, cachedTokens, samples, locale }: { rates: number[]; inputTokens: number[]; cachedTokens: number[]; samples: number[]; locale: "zh" | "en" }) {
  const hasUsage = samples.some((value) => value > 0);
  return (
    <div className="mt-5 border-t border-border pt-4">
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2 text-copy-13 text-fg-muted"><span className="size-1.5 shrink-0 rounded-full bg-green" /><span className="truncate">{locale === "zh" ? "缓存 Token 复用趋势" : "Cache token reuse trend"}</span></div>
        <span className="shrink-0 text-[11px] text-fg-subtle">{locale === "zh" ? "成功对话请求" : "Successful conversations"}</span>
      </div>
      <div role="img" aria-label={locale === "zh" ? "过去 24 小时缓存 Token 复用率趋势" : "Cache token reuse rate over the last 24 hours"} className="mt-3 flex h-14 items-end gap-1 border-b border-border">
        {rates.map((rate, index) => {
          const valid = samples[index] > 0;
          const title = valid
            ? `${rate.toFixed(1)}% · ${formatNumber(cachedTokens[index])} / ${formatNumber(inputTokens[index])} ${locale === "zh" ? "输入 Token" : "input tokens"}`
            : locale === "zh" ? "无 usage 样本" : "No usage samples";
          return <div key={index} className="flex h-full min-w-0 flex-1 items-end" title={title}><span className={`block w-full rounded-t-[2px] ${valid ? "bg-green opacity-80" : "bg-border"}`} style={{ height: valid ? `${Math.max(3, rate)}%` : "1px" }} /></div>;
        })}
      </div>
      {!hasUsage ? <p className="mt-2 text-copy-13 text-fg-subtle">{locale === "zh" ? "当前窗口尚无可计算的 Token usage。" : "No token usage is available in this window."}</p> : null}
    </div>
  );
}

export function DashboardView() {
  const { t, locale } = useI18n();
  const resource = useResource<DashboardData>("/dashboard", initialDashboard);
  const data = resource.data;
  const hourlyRequests = hourSeries(data.hourly_requests);
  const hourlyInputTokens = hourSeries(data.hourly_input_tokens);
  const hourlyCachedTokens = hourSeries(data.hourly_cached_tokens);
  const hourlyCacheSamples = hourSeries(data.hourly_cache_samples ?? data.hourly_usage_samples);
  const hourlyCacheTokenReuseRate = hourSeries(data.hourly_cache_token_reuse_rate ?? data.hourly_cache_hit_rate, 100);
  const recentLogs = data.recent_logs ?? [];
  const maxBar = Math.max(...hourlyRequests, 1);
  const cacheTokenReuseRate = data.cache_token_reuse_rate ?? data.cache_hit_rate ?? 0;
  const cacheSamples = data.cache_samples_24h ?? data.usage_samples_24h ?? 0;
  const cacheRequestHits = data.cache_request_hits_24h ?? 0;
  const cacheRequestHitRate = data.cache_request_hit_rate ?? 0;
  const cacheEligibleRequests = data.cache_eligible_requests_24h ?? 0;
  const cacheUsageCoverage = data.cache_usage_coverage ?? 0;
  const cacheWarmupCandidates = data.cache_warmup_candidates_24h ?? 0;
  const cacheAffinityReuses = data.cache_affinity_reuses_24h ?? 0;
  const cacheAffinityMisses = data.cache_affinity_misses_24h ?? 0;
  const cacheAffinityMissRate = data.cache_affinity_miss_rate ?? 0;
  const cacheAffinityMissValue = cacheAffinityReuses > 0 ? `${cacheAffinityMissRate.toFixed(1)}%` : "-";
  const cacheAffinityMissDetail = cacheAffinityReuses > 0
    ? `${formatNumber(cacheAffinityMisses)} / ${formatNumber(cacheAffinityReuses)} ${locale === "zh" ? "次亲和复用" : "affinity reuses"}`
    : locale === "zh" ? "暂无亲和复用样本" : "No affinity reuse samples";
  const cacheDetail = cacheSamples > 0
    ? `${formatNumber(data.cached_tokens_24h)} / ${formatNumber(data.input_tokens_24h)} ${locale === "zh" ? "输入 Token" : "input tokens"}`
    : locale === "zh" ? "暂无有效 usage" : "No valid usage";
  return (
    <ContentFrame>
      <PageHeader title={t("dashboard.title")} description={t("dashboard.description")} actions={<><Badge tone={data.gateway_healthy ? "green" : "red"} dot>{data.gateway_healthy ? (locale === "zh" ? "网关正常" : "Gateway Healthy") : (locale === "zh" ? "网关异常" : "Gateway Degraded")}</Badge><Button variant="secondary" size="small" onClick={() => void resource.reload()} loading={resource.loading}><RefreshCw className="size-3.5" />{t("common.refresh")}</Button></>} />
      {resource.error ? <ErrorState title={t("common.requestFailed")} description={resource.error.message} onRetry={() => void resource.reload()} /> : null}
      <section aria-label={locale === "zh" ? "关键运行指标" : "Key metrics"} className="grid grid-cols-2 border-t border-border md:grid-cols-5">
        <Stat label={locale === "zh" ? "24 小时请求" : "Requests (24h)"} value={formatNumber(data.requests_24h)} detail={locale === "zh" ? "滚动时间窗口" : "Rolling window"} icon={Zap} />
        <Stat label={locale === "zh" ? "成功率" : "Success Rate"} value={`${data.success_rate.toFixed(2)}%`} detail={locale === "zh" ? "所有公开端点" : "All public endpoints"} icon={ShieldCheck} />
        <Stat label={locale === "zh" ? "平均延迟" : "Average Latency"} value={`${formatNumber(data.avg_latency_ms)} ms`} detail={locale === "zh" ? "端到端平均值" : "End-to-end average"} icon={Clock3} />
        <Stat label={locale === "zh" ? "Token 用量" : "Token Usage"} value={formatNumber(data.tokens_24h)} detail={locale === "zh" ? "输入与输出合计" : "Input and output"} icon={Activity} />
        <Stat className="col-span-2 border-b-0 md:col-span-1 md:border-r-0" label={locale === "zh" ? "缓存 Token 复用率" : "Cache Token Reuse Rate"} value={`${cacheTokenReuseRate.toFixed(1)}%`} detail={cacheDetail} icon={Database} accent />
      </section>
      <div className="grid border-b border-border lg:grid-cols-[minmax(0,1.5fr)_minmax(320px,1fr)]">
        <section aria-labelledby="traffic-heading" className="bg-surface px-4 py-5 sm:px-6 lg:border-r lg:border-border lg:px-8">
          <div className="flex items-center justify-between"><div><h2 id="traffic-heading" className="text-heading-16 font-semibold">{locale === "zh" ? "请求趋势" : "Request Trend"}</h2><p className="text-copy-13 text-fg-muted">{locale === "zh" ? "过去 24 小时，每小时" : "Hourly over the last 24 hours"}</p></div><Link href="/logs/" className={buttonVariants({ variant: "tertiary", size: "small" })}>{locale === "zh" ? "查看日志" : "View Logs"}<ArrowUpRight className="size-3.5" /></Link></div>
          <div role="img" aria-label={locale === "zh" ? "过去 24 小时请求量" : "Request volume over the last 24 hours"} className="mt-6 flex h-32 items-end gap-1.5 border-b border-border pb-0.5">
            {hourlyRequests.map((value, index) => <div key={index} title={`${formatNumber(value)} ${locale === "zh" ? "次请求" : "requests"}`} className="min-h-px flex-1 rounded-t-[2px] bg-blue-700 opacity-80 transition-opacity hover:opacity-100" style={{ height: `${Math.max(1, (value / maxBar) * 100)}%` }} />)}
          </div>
          <div className="mt-2 flex justify-between text-[11px] text-fg-subtle"><span>{locale === "zh" ? "24 小时前" : "24h ago"}</span><span>{locale === "zh" ? "12 小时前" : "12h ago"}</span><span>{locale === "zh" ? "当前" : "Now"}</span></div>
          <CacheTrend rates={hourlyCacheTokenReuseRate} inputTokens={hourlyInputTokens} cachedTokens={hourlyCachedTokens} samples={hourlyCacheSamples} locale={locale} />
          <div className="mt-4 grid grid-cols-2 border-y border-border 2xl:grid-cols-4">
            <div className="min-w-0 border-b border-r border-border py-3 pr-3 2xl:border-b-0"><p className="text-label-13 text-fg-muted">{locale === "zh" ? "缓存请求命中率" : "Request Hit Rate"}</p><strong className="mt-1 block font-mono text-heading-20 tabular-nums text-fg">{cacheRequestHitRate.toFixed(1)}%</strong><p className="text-copy-13 leading-5 text-fg-subtle">{formatNumber(cacheRequestHits)} / {formatNumber(cacheSamples)} {locale === "zh" ? "次请求" : "requests"}</p></div>
            <div className="min-w-0 border-b border-border py-3 pl-3 2xl:border-b-0 2xl:border-r 2xl:pr-3"><p className="text-label-13 text-fg-muted">{locale === "zh" ? "缓存预热候选" : "Warmup Candidates"}</p><strong className="mt-1 block font-mono text-heading-20 tabular-nums text-fg">{formatNumber(cacheWarmupCandidates)}</strong><p className="text-copy-13 leading-5 text-fg-subtle">{locale === "zh" ? "新建亲和绑定，缓存尚未命中" : "New affinity binding, cache not hit yet"}</p></div>
            <div className="min-w-0 border-r border-border py-3 pr-3"><p className="text-label-13 text-fg-muted">{locale === "zh" ? "亲和复用未命中率" : "Affinity Reuse Miss Rate"}</p><strong className="mt-1 block font-mono text-heading-20 tabular-nums text-fg">{cacheAffinityMissValue}</strong><p className="text-copy-13 leading-5 text-fg-subtle">{cacheAffinityMissDetail}</p></div>
            <div className="min-w-0 py-3 pl-3"><p className="text-label-13 text-fg-muted">{locale === "zh" ? "Usage 覆盖率" : "Usage Coverage"}</p><strong className="mt-1 block font-mono text-heading-20 tabular-nums text-fg">{cacheUsageCoverage.toFixed(1)}%</strong><p className="text-copy-13 leading-5 text-fg-subtle">{formatNumber(data.usage_samples_24h)} / {formatNumber(cacheEligibleRequests)} {locale === "zh" ? "次请求" : "requests"}</p></div>
          </div>
        </section>
        <section aria-labelledby="capacity-heading" className="bg-surface px-4 py-5 sm:px-6 lg:px-8">
          <h2 id="capacity-heading" className="text-heading-16 font-semibold">{locale === "zh" ? "当前容量" : "Current Capacity"}</h2><p className="text-copy-13 text-fg-muted">{locale === "zh" ? "可调度资源" : "Schedulable resources"}</p>
          <div className="mt-4 divide-y divide-border border-y border-border">
            <Link href="/accounts/" className="flex items-center gap-3 py-3.5 outline-none transition-colors hover:text-blue focus-visible:shadow-focus"><Users className="size-4 text-fg-muted" /><span className="text-copy-14">{locale === "zh" ? "活跃账号" : "Active accounts"}</span><strong className="ml-auto font-mono text-label-14 tabular-nums">{data.active_accounts} / {data.total_accounts}</strong><ArrowUpRight className="size-3.5 text-fg-subtle" /></Link>
            <Link href="/keys/" className="flex items-center gap-3 py-3.5 outline-none transition-colors hover:text-blue focus-visible:shadow-focus"><KeyRound className="size-4 text-fg-muted" /><span className="text-copy-14">{locale === "zh" ? "已启用密钥" : "Enabled keys"}</span><strong className="ml-auto font-mono text-label-14 tabular-nums">{data.active_keys}</strong><ArrowUpRight className="size-3.5 text-fg-subtle" /></Link>
            <div className="flex items-center gap-3 py-3.5"><Activity className="size-4 text-fg-muted" /><span className="text-copy-14">{locale === "zh" ? "网关状态" : "Gateway status"}</span><Badge className="ml-auto" tone={data.gateway_healthy ? "green" : "red"} dot>{data.gateway_healthy ? (locale === "zh" ? "正常" : "Operational") : (locale === "zh" ? "异常" : "Degraded")}</Badge></div>
          </div>
        </section>
      </div>
      <section aria-labelledby="recent-heading" className="bg-surface">
        <div className="flex h-12 items-center justify-between px-4 sm:px-6 lg:px-8"><h2 id="recent-heading" className="text-label-14 font-semibold">{locale === "zh" ? "最近请求" : "Recent Requests"}</h2><span className="text-copy-13 tabular-nums text-fg-subtle">{recentLogs.length} {locale === "zh" ? "条" : "events"}</span></div>
        {recentLogs.length ? <div className="divide-y divide-border border-y border-border">{recentLogs.slice(0, 6).map((log) => <div key={log.id} className="grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-3 px-4 py-3 sm:grid-cols-[150px_minmax(0,1fr)_80px_76px_100px] sm:px-6 lg:px-8"><code className="truncate text-label-13 text-fg">{log.request_id}</code><span className="hidden truncate text-copy-13 text-fg-muted sm:block">{log.model || log.endpoint}</span><Badge tone={log.status_code >= 500 ? "red" : log.status_code >= 400 ? "amber" : "green"}>{log.status_code}</Badge><span className="font-mono text-label-13 tabular-nums text-fg-muted">{log.duration_ms} ms</span><span className="hidden text-right text-copy-13 text-fg-subtle sm:block">{formatRelative(log.created_at, locale)}</span></div>)}</div> : <div className="border-y border-border px-6 py-10 text-center text-copy-14 text-fg-muted">{locale === "zh" ? "尚无请求记录。" : "No requests recorded yet."}</div>}
      </section>
    </ContentFrame>
  );
}
