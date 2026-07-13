"use client";

import { useDeferredValue, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Eye, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button, IconButton } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { Select } from "@/components/ui/form";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, ErrorState, LoadingState } from "@/components/ui/feedback";
import { useToast } from "@/components/ui/toast";
import { useI18n } from "@/components/i18n-provider";
import { apiFetch } from "@/lib/api";
import { formatNumber, formatRelative } from "@/lib/format";
import type { ListResponse, RequestLog } from "@/lib/types";
import { normalizeList } from "@/lib/types";
import { useResource } from "@/lib/use-resource";
import { DEFAULT_PAGE_SIZE, paginatedPath } from "@/lib/pagination";
import { usePageClamp } from "@/lib/use-page-clamp";
import { ContentFrame, FormActions, PageFooter, Toolbar } from "./shared";

function statusTone(code: number): "green" | "amber" | "red" | "neutral" {
  if (code >= 500) return "red";
  if (code >= 400) return "amber";
  if (code >= 200 && code < 400) return "green";
  return "neutral";
}

const timeWindows: Record<string, number> = {
  hour: 60 * 60 * 1000,
  day: 24 * 60 * 60 * 1000,
  week: 7 * 24 * 60 * 60 * 1000,
};

interface AuditLog {
  id: string;
  admin_id?: string;
  action: string;
  resource_type: string;
  resource_id?: string;
  ip_address?: string;
  metadata: { method?: string; route?: string; status?: number; success?: boolean; duration_ms?: number };
  created_at: string;
}

export function LogsView() {
  const { t, locale } = useI18n();
  const [view, setView] = useState("requests");
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");
  const [modelFilter, setModelFilter] = useState("all");
  const [timeFilter, setTimeFilter] = useState("day");
  const [referenceTime] = useState(() => Date.now());
  const [requestPage, setRequestPage] = useState(1);
  const [requestPageSize, setRequestPageSize] = useState(DEFAULT_PAGE_SIZE);
  const [selected, setSelected] = useState<RequestLog | null>(null);
  const [cleanupOpen, setCleanupOpen] = useState(false);
  const [auditCleanupOpen, setAuditCleanupOpen] = useState(false);
  const [auditSearch, setAuditSearch] = useState("");
  const [auditResourceType, setAuditResourceType] = useState("all");
  const [auditResult, setAuditResult] = useState("all");
  const [auditTime, setAuditTime] = useState("week");
  const [auditPage, setAuditPage] = useState(1);
  const [auditPageSize, setAuditPageSize] = useState(DEFAULT_PAGE_SIZE);
  const [selectedAudit, setSelectedAudit] = useState<AuditLog | null>(null);
  const deferredSearch = useDeferredValue(search);
  const deferredAuditSearch = useDeferredValue(auditSearch);
  const requestPath = useMemo(() => paginatedPath("/logs", requestPage, requestPageSize, {
    q: deferredSearch,
    status_class: statusFilter === "all" ? "" : statusFilter,
    model: modelFilter === "all" ? "" : modelFilter,
    created_from: timeFilter === "all" ? "" : new Date(referenceTime - timeWindows[timeFilter]).toISOString(),
  }), [deferredSearch, modelFilter, referenceTime, requestPage, requestPageSize, statusFilter, timeFilter]);
  const auditPath = useMemo(() => paginatedPath("/audit-logs", auditPage, auditPageSize, {
    q: deferredAuditSearch,
    resource_type: auditResourceType === "all" ? "" : auditResourceType,
    success: auditResult === "all" ? "" : auditResult === "success",
    created_from: auditTime === "all" ? "" : new Date(referenceTime - timeWindows[auditTime]).toISOString(),
  }), [auditPage, auditPageSize, auditResourceType, auditResult, auditTime, deferredAuditSearch, referenceTime]);
  const resource = useResource<RequestLog[] | ListResponse<RequestLog>>(requestPath, []);
  const auditResource = useResource<AuditLog[] | ListResponse<AuditLog>>(auditPath, []);
  const requestList = normalizeList(resource.data);
  const auditList = normalizeList(auditResource.data);
  const allRows = requestList.items;
  const allAuditRows = auditList.items;
  const requestTotal = requestList.total ?? allRows.length;
  const auditTotal = auditList.total ?? allAuditRows.length;
  usePageClamp(requestPage, requestPageSize, requestTotal, setRequestPage);
  usePageClamp(auditPage, auditPageSize, auditTotal, setAuditPage);

  const models = useMemo(
    () => Array.from(new Set([...(modelFilter === "all" ? [] : [modelFilter]), ...allRows.map((log) => log.model).filter((value): value is string => Boolean(value))])).sort(),
    [allRows, modelFilter],
  );

  const rows = allRows;

  const columns = useMemo<ColumnDef<RequestLog>[]>(() => [
    { accessorKey: "created_at", header: locale === "zh" ? "时间" : "Time", cell: ({ row }) => <span className="whitespace-nowrap text-copy-13 text-fg-muted" title={new Date(row.original.created_at).toLocaleString()}>{formatRelative(row.original.created_at, locale)}</span> },
    { accessorKey: "request_id", header: "Request ID", cell: ({ row }) => <code className="block max-w-[150px] truncate text-label-13" title={row.original.request_id}>{row.original.request_id}</code> },
    { accessorKey: "endpoint", header: locale === "zh" ? "端点" : "Endpoint", cell: ({ row }) => <code className="block max-w-[220px] truncate text-label-13" title={row.original.endpoint}>{row.original.endpoint}</code> },
    { accessorKey: "model", header: locale === "zh" ? "模型" : "Model", cell: ({ row }) => <span className="block max-w-[180px] truncate text-copy-13" title={row.original.model}>{row.original.model || "-"}</span> },
    { accessorKey: "status_code", header: locale === "zh" ? "状态码" : "Status", cell: ({ row }) => <Badge tone={statusTone(row.original.status_code)}>{row.original.status_code}</Badge> },
    { accessorKey: "duration_ms", header: locale === "zh" ? "延迟" : "Latency", cell: ({ row }) => <span className="whitespace-nowrap font-mono text-label-13 tabular-nums">{formatNumber(row.original.duration_ms)} ms</span> },
    { id: "tokens", header: "Tokens", cell: ({ row }) => <span className="font-mono text-label-13 tabular-nums">{formatNumber(row.original.input_tokens + row.original.output_tokens)}</span> },
    { id: "actions", enableSorting: false, header: () => <span className="sr-only">{t("common.actions")}</span>, cell: ({ row }) => <IconButton label={locale === "zh" ? "查看请求详情" : "View request details"} variant="tertiary" onClick={() => setSelected(row.original)}><Eye className="size-4" /></IconButton> },
  ], [locale, t]);

  const auditResourceTypes = useMemo(() => Array.from(new Set([...(auditResourceType === "all" ? [] : [auditResourceType]), ...allAuditRows.map((log) => log.resource_type)])).sort(), [allAuditRows, auditResourceType]);
  const auditRows = allAuditRows;
  const auditColumns = useMemo<ColumnDef<AuditLog>[]>(() => [
    { accessorKey: "created_at", header: locale === "zh" ? "时间" : "Time", cell: ({ row }) => <span className="whitespace-nowrap text-copy-13 text-fg-muted" title={new Date(row.original.created_at).toLocaleString()}>{formatRelative(row.original.created_at, locale)}</span> },
    { accessorKey: "admin_id", header: locale === "zh" ? "管理员" : "Administrator", cell: ({ row }) => <code className="block max-w-[150px] truncate text-label-13">{row.original.admin_id || "-"}</code> },
    { accessorKey: "action", header: locale === "zh" ? "操作" : "Action", cell: ({ row }) => <code className="text-label-13 font-medium">{row.original.action}</code> },
    { accessorKey: "resource_type", header: locale === "zh" ? "资源" : "Resource", cell: ({ row }) => <div className="grid gap-0.5"><span className="text-copy-13">{row.original.resource_type}</span><code className="block max-w-[180px] truncate text-[11px] text-fg-subtle">{row.original.resource_id || "-"}</code></div> },
    { accessorKey: "ip_address", header: "IP", cell: ({ row }) => <code className="text-label-13 text-fg-muted">{row.original.ip_address || "-"}</code> },
    { id: "result", header: locale === "zh" ? "结果" : "Result", cell: ({ row }) => <Badge tone={row.original.metadata?.success === false ? "red" : "green"} dot>{row.original.metadata?.status ?? "-"}</Badge> },
    { id: "actions", enableSorting: false, header: () => <span className="sr-only">{t("common.actions")}</span>, cell: ({ row }) => <IconButton label={locale === "zh" ? "查看审计详情" : "View audit details"} variant="tertiary" onClick={() => setSelectedAudit(row.original)}><Eye className="size-4" /></IconButton> },
  ], [locale, t]);

  return (
    <ContentFrame>
      <PageHeader title={locale === "zh" ? "日志中心" : "Logs"} description={view === "requests" ? t("log.description") : (locale === "zh" ? "追踪管理员变更、资源目标、来源 IP 与执行结果。" : "Trace administrator changes, resource targets, source IPs, and outcomes.")} actions={<><Button size="small" variant="secondary" onClick={() => view === "requests" ? setCleanupOpen(true) : setAuditCleanupOpen(true)}><Trash2 className="size-3.5" />{locale === "zh" ? "清理日志" : "Clean Up"}</Button><Button size="small" variant="secondary" onClick={() => void (view === "requests" ? resource.reload() : auditResource.reload())} loading={view === "requests" ? resource.loading : auditResource.loading}><RefreshCw className="size-3.5" />{t("common.refresh")}</Button></>} />
      <Tabs value={view} onValueChange={setView}><div className="border-y border-border bg-surface px-4 py-2 sm:px-6 lg:px-8"><TabsList aria-label={locale === "zh" ? "日志类型" : "Log type"}><TabsTrigger value="requests">{locale === "zh" ? "请求日志" : "Request Logs"}</TabsTrigger><TabsTrigger value="audit"><ShieldCheck className="size-3.5" />{locale === "zh" ? "操作审计" : "Audit Trail"}</TabsTrigger></TabsList></div></Tabs>
      {view === "requests" ? <>
      <Toolbar
        search={search}
        onSearch={(value) => { setSearch(value); setRequestPage(1); }}
        placeholder={locale === "zh" ? "搜索请求 ID、模型或错误码" : "Search request ID, model, or error"}
        filter={statusFilter}
        onFilter={(value) => { setStatusFilter(value); setRequestPage(1); }}
        filterOptions={[
          { value: "all", label: locale === "zh" ? "全部结果" : "All results" },
          { value: "2xx", label: "2xx" },
          { value: "4xx", label: "4xx" },
          { value: "5xx", label: "5xx" },
        ]}
        filters={<><div className="w-full sm:w-44"><Select aria-label={locale === "zh" ? "按模型筛选" : "Filter by model"} value={modelFilter} onChange={(event) => { setModelFilter(event.target.value); setRequestPage(1); }} options={[{ value: "all", label: locale === "zh" ? "全部模型" : "All models" }, ...models.map((model) => ({ value: model, label: model }))]} /></div><div className="w-full sm:w-36"><Select aria-label={locale === "zh" ? "按时间筛选" : "Filter by time"} value={timeFilter} onChange={(event) => { setTimeFilter(event.target.value); setRequestPage(1); }} options={[{ value: "hour", label: locale === "zh" ? "最近 1 小时" : "Last hour" }, { value: "day", label: locale === "zh" ? "最近 24 小时" : "Last 24 hours" }, { value: "week", label: locale === "zh" ? "最近 7 天" : "Last 7 days" }, { value: "all", label: locale === "zh" ? "全部时间" : "All time" }]} /></div></>}
        trailing={<span className="text-copy-13 tabular-nums text-fg-subtle">{rows.length} / {requestTotal}</span>}
      />
      {resource.loading && !allRows.length ? <LoadingState label={t("common.loading")} /> : resource.error ? <ErrorState title={t("common.requestFailed")} description={resource.error.message} onRetry={() => void resource.reload()} /> : rows.length ? <DataTable ariaLabel={t("log.title")} data={rows} columns={columns} getRowId={(row) => row.id} /> : <EmptyState title={locale === "zh" ? "没有匹配的请求" : "No matching requests"} description={locale === "zh" ? "调整搜索、模型、时间或状态码筛选条件。" : "Adjust the search, model, time, or status-code filters."} />}
      <PageFooter count={rows.length} noun={locale === "zh" ? "条请求" : "requests"} total={requestTotal} page={requestPage} pageSize={requestPageSize} onPageChange={setRequestPage} onPageSizeChange={(value) => { setRequestPageSize(value); setRequestPage(1); }} locale={locale} />
      </> : <>
        <Toolbar search={auditSearch} onSearch={(value) => { setAuditSearch(value); setAuditPage(1); }} placeholder={locale === "zh" ? "搜索操作、资源、管理员或 IP" : "Search action, resource, administrator, or IP"} filter={auditResourceType} onFilter={(value) => { setAuditResourceType(value); setAuditPage(1); }} filterOptions={[{ value: "all", label: locale === "zh" ? "全部资源" : "All resources" }, ...auditResourceTypes.map((value) => ({ value, label: value }))]} filters={<><div className="w-full sm:w-36"><Select aria-label={locale === "zh" ? "按结果筛选" : "Filter by result"} value={auditResult} onChange={(event) => { setAuditResult(event.target.value); setAuditPage(1); }} options={[{ value: "all", label: locale === "zh" ? "全部结果" : "All results" }, { value: "success", label: locale === "zh" ? "成功" : "Succeeded" }, { value: "failed", label: locale === "zh" ? "失败" : "Failed" }]} /></div><div className="w-full sm:w-36"><Select aria-label={locale === "zh" ? "按审计时间筛选" : "Filter audit time"} value={auditTime} onChange={(event) => { setAuditTime(event.target.value); setAuditPage(1); }} options={[{ value: "day", label: locale === "zh" ? "最近 24 小时" : "Last 24 hours" }, { value: "week", label: locale === "zh" ? "最近 7 天" : "Last 7 days" }, { value: "all", label: locale === "zh" ? "全部时间" : "All time" }]} /></div></>} trailing={<span className="text-copy-13 tabular-nums text-fg-subtle">{auditRows.length} / {auditTotal}</span>} />
        {auditResource.loading && !allAuditRows.length ? <LoadingState label={t("common.loading")} /> : auditResource.error ? <ErrorState title={t("common.requestFailed")} description={auditResource.error.message} onRetry={() => void auditResource.reload()} /> : auditRows.length ? <DataTable ariaLabel={locale === "zh" ? "操作审计" : "Audit Trail"} data={auditRows} columns={auditColumns} getRowId={(row) => row.id} /> : <EmptyState title={locale === "zh" ? "没有匹配的审计记录" : "No matching audit entries"} description={locale === "zh" ? "调整资源、结果、时间或搜索条件。" : "Adjust the resource, result, time, or search filters."} />}
        <PageFooter count={auditRows.length} noun={locale === "zh" ? "条审计" : "audit entries"} total={auditTotal} page={auditPage} pageSize={auditPageSize} onPageChange={setAuditPage} onPageSizeChange={(value) => { setAuditPageSize(value); setAuditPage(1); }} locale={locale} />
      </>}

      <Dialog open={Boolean(selected)} onOpenChange={(open) => { if (!open) setSelected(null); }}>
        <DialogContent title={locale === "zh" ? "请求详情" : "Request Details"} description={selected?.request_id} className="max-w-2xl">
          {selected ? <div className="grid gap-5 px-5 py-5"><dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">{[[locale === "zh" ? "状态码" : "Status", selected.status_code], [locale === "zh" ? "延迟" : "Latency", `${selected.duration_ms} ms`], [locale === "zh" ? "输入 Token" : "Input Tokens", formatNumber(selected.input_tokens)], [locale === "zh" ? "输出 Token" : "Output Tokens", formatNumber(selected.output_tokens)], [locale === "zh" ? "缓存 Token" : "Cached Tokens", formatNumber(selected.cached_tokens)], [locale === "zh" ? "账号 ID" : "Account ID", selected.account_id || "-"], [locale === "zh" ? "密钥 ID" : "Key ID", selected.client_key_id || "-"], [locale === "zh" ? "请求时间" : "Requested At", new Date(selected.created_at).toLocaleString(locale === "zh" ? "zh-CN" : "en")]].map(([label, value]) => <div key={String(label)}><dt className="text-label-13 text-fg-subtle">{label}</dt><dd className="mt-0.5 break-all font-mono text-label-13 text-fg">{value}</dd></div>)}</dl>{selected.error_summary ? <div className="rounded-[6px] border border-red-soft bg-red-soft p-3"><p className="text-label-13 font-medium text-danger">{selected.error_code || "Error"}</p><p className="mt-1 text-copy-13 text-fg-muted">{selected.error_summary}</p></div> : null}<div><h3 className="mb-2 text-label-13 font-medium">Metadata</h3><pre className="scrollbar max-h-64 overflow-auto rounded-[6px] border border-border bg-subtle p-3 font-mono text-xs leading-5 text-fg">{JSON.stringify(selected.metadata ?? {}, null, 2)}</pre></div></div> : null}
        </DialogContent>
      </Dialog>

      <Dialog open={cleanupOpen} onOpenChange={setCleanupOpen}>
        <CleanupLogsDialog onCleaned={async () => { setCleanupOpen(false); await resource.reload(); }} />
      </Dialog>
      <Dialog open={Boolean(selectedAudit)} onOpenChange={(open) => { if (!open) setSelectedAudit(null); }}>
        <DialogContent title={locale === "zh" ? "审计详情" : "Audit Details"} description={selectedAudit?.action} className="max-w-xl">{selectedAudit ? <div className="grid gap-4 px-5 py-5"><dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">{[[locale === "zh" ? "管理员" : "Administrator", selectedAudit.admin_id || "-"], [locale === "zh" ? "资源" : "Resource", `${selectedAudit.resource_type}${selectedAudit.resource_id ? ` / ${selectedAudit.resource_id}` : ""}`], [locale === "zh" ? "来源 IP" : "Source IP", selectedAudit.ip_address || "-"], [locale === "zh" ? "请求方法" : "Method", selectedAudit.metadata?.method || "-"], [locale === "zh" ? "响应状态" : "Status", selectedAudit.metadata?.status ?? "-"], [locale === "zh" ? "耗时" : "Duration", `${selectedAudit.metadata?.duration_ms ?? 0} ms`], [locale === "zh" ? "路由" : "Route", selectedAudit.metadata?.route || "-"], [locale === "zh" ? "时间" : "Time", new Date(selectedAudit.created_at).toLocaleString(locale === "zh" ? "zh-CN" : "en")]].map(([label, value]) => <div key={String(label)}><dt className="text-label-13 text-fg-subtle">{label}</dt><dd className="mt-0.5 break-all font-mono text-label-13 text-fg">{value}</dd></div>)}</dl><p className="border-t border-border pt-3 text-copy-13 text-fg-subtle">{locale === "zh" ? "审计记录仅包含路由、状态和资源标识，不保存请求正文或凭据。" : "Audit entries contain route, status, and resource identifiers only; request bodies and credentials are not stored."}</p></div> : null}</DialogContent>
      </Dialog>
      <Dialog open={auditCleanupOpen} onOpenChange={setAuditCleanupOpen}>
        <AuditCleanupDialog onCleaned={async () => { setAuditCleanupOpen(false); await auditResource.reload(); }} />
      </Dialog>
    </ContentFrame>
  );
}

function AuditCleanupDialog({ onCleaned }: { onCleaned: () => Promise<void> }) {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [retention, setRetention] = useState("180");
  const [cleaning, setCleaning] = useState(false);
  const [error, setError] = useState("");
  async function cleanup() {
    setCleaning(true); setError("");
    const before = new Date(Date.now() - Number(retention) * 24 * 60 * 60 * 1000);
    try {
      const result = await apiFetch<{ deleted: number }>(`/audit-logs?before=${encodeURIComponent(before.toISOString())}`, { method: "DELETE" });
      toast(locale === "zh" ? `已清理 ${result.deleted.toLocaleString()} 条审计记录` : `Removed ${result.deleted.toLocaleString()} audit entries`);
      await onCleaned();
    } catch (reason) { setError(reason instanceof Error ? reason.message : t("common.requestFailed")); }
    finally { setCleaning(false); }
  }
  return <DialogContent title={locale === "zh" ? "清理操作审计" : "Clean Up Audit Trail"} description={locale === "zh" ? "删除早于保留周期的记录，本次清理操作仍会被审计。" : "Delete entries older than the retention window. This cleanup remains audited."}><div className="grid gap-4 px-5 py-5"><Select label={locale === "zh" ? "保留周期" : "Retention"} value={retention} onChange={(event) => setRetention(event.target.value)} options={[{ value: "30", label: locale === "zh" ? "保留最近 30 天" : "Keep 30 days" }, { value: "90", label: locale === "zh" ? "保留最近 90 天" : "Keep 90 days" }, { value: "180", label: locale === "zh" ? "保留最近 180 天" : "Keep 180 days" }, { value: "365", label: locale === "zh" ? "保留最近 1 年" : "Keep 1 year" }]} />{error ? <p role="alert" className="text-copy-13 text-danger">{error}</p> : null}</div><FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button loading={cleaning} onClick={() => void cleanup()}><Trash2 className="size-4" />{locale === "zh" ? "执行清理" : "Clean Up"}</Button></FormActions></DialogContent>;
}

function CleanupLogsDialog({ onCleaned }: { onCleaned: () => Promise<void> }) {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [retention, setRetention] = useState("30");
  const [cleaning, setCleaning] = useState(false);
  const [error, setError] = useState("");

  async function cleanup() {
    setCleaning(true);
    setError("");
    const before = retention === "all" ? new Date(Date.now() + 60_000) : new Date(Date.now() - Number(retention) * 24 * 60 * 60 * 1000);
    try {
      const result = await apiFetch<{ deleted: number }>(`/logs?before=${encodeURIComponent(before.toISOString())}`, { method: "DELETE" });
      toast(locale === "zh" ? `已清理 ${result.deleted.toLocaleString()} 条日志` : `Removed ${result.deleted.toLocaleString()} logs`);
      await onCleaned();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setCleaning(false);
    }
  }

  return (
    <DialogContent title={locale === "zh" ? "清理请求日志" : "Clean Up Request Logs"} description={locale === "zh" ? "选择保留周期，较早的日志将永久删除。" : "Choose a retention window. Older logs are permanently removed."}>
      <div className="grid gap-4 px-5 py-5"><Select label={locale === "zh" ? "保留周期" : "Retention"} value={retention} onChange={(event) => setRetention(event.target.value)} options={[{ value: "7", label: locale === "zh" ? "保留最近 7 天" : "Keep 7 days" }, { value: "30", label: locale === "zh" ? "保留最近 30 天" : "Keep 30 days" }, { value: "90", label: locale === "zh" ? "保留最近 90 天" : "Keep 90 days" }, { value: "all", label: locale === "zh" ? "清空全部日志" : "Remove all logs" }]} />{retention === "all" ? <p className="rounded-[6px] border border-red-soft bg-red-soft p-3 text-copy-13 text-danger">{locale === "zh" ? "全部请求日志都将被删除。" : "Every request log will be deleted."}</p> : null}{error ? <p role="alert" className="text-copy-13 text-danger">{error}</p> : null}</div>
      <FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button variant={retention === "all" ? "danger" : "primary"} loading={cleaning} onClick={() => void cleanup()}><Trash2 className="size-4" />{locale === "zh" ? "执行清理" : "Clean Up"}</Button></FormActions>
    </DialogContent>
  );
}
