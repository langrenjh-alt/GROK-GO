"use client";

import { useDeferredValue, useMemo, useRef, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Activity, CircleCheck, CircleX, ExternalLink, FileJson, KeyRound, Pencil, Plus, Power, PowerOff, RefreshCw, Upload, UsersRound, X } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Button, IconButton } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { Input, Select, Textarea } from "@/components/ui/form";
import { EmptyState, ErrorState, LoadingState } from "@/components/ui/feedback";
import { useToast } from "@/components/ui/toast";
import { useI18n } from "@/components/i18n-provider";
import { apiFetch, jsonBody } from "@/lib/api";
import { formatRelative } from "@/lib/format";
import type { Account, AccountProbeBatchResult, AccountProbeResult, AccountQuotaMetricSummary, AccountQuotaSummary, AccountSchedulingStrategy, AccountStatus, CredentialKind, ListResponse, ProxyRecord } from "@/lib/types";
import { normalizeList } from "@/lib/types";
import { useResource } from "@/lib/use-resource";
import { DEFAULT_PAGE_SIZE, paginatedPath } from "@/lib/pagination";
import { usePageClamp } from "@/lib/use-page-clamp";
import { AccountStatusBadge, ContentFrame, FormActions, PageFooter, QuotaBar, Toolbar } from "./shared";

interface OAuthSession {
  authorization_url: string;
  state: string;
  verifier?: string;
}

interface ImportResult {
  imported: number;
  failed: number;
  errors?: string[];
}

const emptyQuotaSummary: AccountQuotaSummary = {
  total_accounts: 0,
  available_accounts: 0,
  requests: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 0, unlimited_accounts: 0, window_count: 0 },
  tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 0, unlimited_accounts: 0, window_count: 0 },
};

export function AccountsView() {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE);
  const deferredSearch = useDeferredValue(search);
  const accountPath = useMemo(() => paginatedPath("/accounts", page, pageSize, { q: deferredSearch, status: filter === "all" ? "" : filter }), [deferredSearch, filter, page, pageSize]);
  const resource = useResource<Account[] | ListResponse<Account>>(accountPath, []);
  const quotaResource = useResource<AccountQuotaSummary>("/accounts/quota-summary", emptyQuotaSummary);
  const policyResource = useResource<{ strategy: AccountSchedulingStrategy }>("/accounts/policy", { strategy: "affinity" });
  const proxyResource = useResource<ProxyRecord[] | ListResponse<ProxyRecord>>("/proxies", []);
  const [open, setOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [importFiles, setImportFiles] = useState<File[]>([]);
  const [oauthOpen, setOAuthOpen] = useState(false);
  const [editAccount, setEditAccount] = useState<Account | null>(null);
  const [batchOpen, setBatchOpen] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [oauthSession, setOAuthSession] = useState<OAuthSession | null>(null);
  const [oauthLoading, setOAuthLoading] = useState(false);
  const [refreshingID, setRefreshingID] = useState("");
  const [updatingID, setUpdatingID] = useState("");
  const [probingIDs, setProbingIDs] = useState<Set<string>>(() => new Set());
  const [batchProbing, setBatchProbing] = useState(false);
  const [probeResult, setProbeResult] = useState<AccountProbeBatchResult | null>(null);
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState("");

  const accountList = normalizeList(resource.data);
  const accountItems = accountList.items;
  const rows = accountItems;
  const total = accountList.total ?? accountItems.length;
  usePageClamp(page, pageSize, total, setPage);
  const allVisibleSelected = rows.length > 0 && rows.every((account) => selected.has(account.id));
  const proxyOptions = useMemo(() => [
    { value: "", label: locale === "zh" ? "直连（不使用代理）" : "Direct (no proxy)" },
    ...(normalizeList(proxyResource.data).items ?? []).map((proxy) => ({ value: proxy.id, label: proxy.name })),
  ], [locale, proxyResource.data]);

  const columns: ColumnDef<Account>[] = [
    {
      id: "select",
      enableSorting: false,
      header: () => <input type="checkbox" aria-label={locale === "zh" ? "选择当前账号" : "Select visible accounts"} checked={allVisibleSelected} onChange={(event) => selectVisible(event.target.checked)} className="size-4 accent-blue-700" />,
      cell: ({ row }) => <input type="checkbox" aria-label={locale === "zh" ? `选择 ${row.original.name}` : `Select ${row.original.name}`} checked={selected.has(row.original.id)} onChange={(event) => selectOne(row.original.id, event.target.checked)} className="size-4 accent-blue-700" />,
    },
    { accessorKey: "name", header: locale === "zh" ? "账号" : "Account", cell: ({ row }) => <div className="min-w-0"><div className="truncate font-medium">{row.original.name}</div><div className="truncate text-copy-13 text-fg-subtle">{row.original.email || row.original.id}</div></div> },
    { accessorKey: "status", header: t("common.status"), cell: ({ row }) => <div className="grid gap-1"><AccountStatusBadge status={row.original.status} /><span className="max-w-36 truncate text-[11px] text-fg-subtle" title={row.original.last_error}>{row.original.failure_count > 0 ? (locale === "zh" ? `${row.original.failure_count} 次失败` : `${row.original.failure_count} failures`) : `${locale === "zh" ? "健康度" : "Health"} ${Math.round(row.original.health_score * 100)}%`}</span></div> },
    { accessorKey: "kind", header: locale === "zh" ? "凭据 / 套餐" : "Credential / Tier", cell: ({ row }) => <div><code className="text-label-13">{row.original.kind}</code><div className="text-copy-13 text-fg-subtle">{row.original.tier || "-"}</div></div> },
    { id: "credential_expiry", header: locale === "zh" ? "凭据到期" : "Credential Expiry", cell: ({ row }) => <CredentialExpiry account={row.original} locale={locale} /> },
    { id: "quota", header: locale === "zh" ? "剩余请求" : "Requests Remaining", cell: ({ row }) => <AccountQuota account={row.original} locale={locale} /> },
    { id: "dispatch", header: locale === "zh" ? "调度" : "Dispatch", cell: ({ row }) => <div className="grid gap-0.5 font-mono text-label-13 tabular-nums"><span>{locale === "zh" ? "优先级" : "Priority"} {row.original.priority}</span><span className="text-fg-subtle">{locale === "zh" ? "并发" : "Concurrency"} {row.original.concurrency_limit}</span></div> },
    { accessorKey: "last_used_at", header: locale === "zh" ? "最近使用" : "Last Used", cell: ({ row }) => <span className="whitespace-nowrap text-copy-13 text-fg-muted">{formatRelative(row.original.last_used_at, locale)}</span> },
    {
      id: "actions",
      enableSorting: false,
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: ({ row }) => <div className="flex items-center justify-end gap-0.5">
        <IconButton label={locale === "zh" ? "探测账号连接" : "Probe account connection"} variant="tertiary" loading={probingIDs.has(row.original.id)} onClick={() => void probeAccount(row.original)}><Activity className="size-4" /></IconButton>
        {row.original.kind === "cli_oauth" ? <IconButton label={locale === "zh" ? "刷新 OAuth 凭据" : "Refresh OAuth credentials"} variant="tertiary" loading={refreshingID === row.original.id} onClick={() => void refreshOAuth(row.original.id)}><RefreshCw className="size-4" /></IconButton> : null}
        <IconButton label={locale === "zh" ? "编辑账号" : "Edit account"} variant="tertiary" onClick={() => { setFormError(""); setEditAccount(row.original); }}><Pencil className="size-4" /></IconButton>
        <IconButton label={row.original.status === "disabled" ? (locale === "zh" ? "启用账号" : "Enable account") : (locale === "zh" ? "停用账号" : "Disable account")} variant="tertiary" loading={updatingID === row.original.id} onClick={() => void toggleAccount(row.original)}>{row.original.status === "disabled" ? <Power className="size-4" /> : <PowerOff className="size-4" />}</IconButton>
      </div>,
    },
  ];

  function selectOne(id: string, checked: boolean) {
    setSelected((current) => {
      const next = new Set(current);
      if (checked) next.add(id); else next.delete(id);
      return next;
    });
  }

  function selectVisible(checked: boolean) {
    setSelected((current) => {
      const next = new Set(current);
      for (const account of rows) {
        if (checked) next.add(account.id); else next.delete(account.id);
      }
      return next;
    });
  }

  async function reloadAccounts() {
    await Promise.all([resource.reload(), quotaResource.reload()]);
  }

  async function probeAccount(account: Account) {
    setProbingIDs((current) => new Set(current).add(account.id));
    try {
      const result = await apiFetch<AccountProbeResult>(`/accounts/${account.id}/probe`, { method: "POST", ...jsonBody({}) });
      setProbeResult({ total: 1, succeeded: result.success ? 1 : 0, failed: result.success ? 0 : 1, items: [result] });
      toast(result.success ? (locale === "zh" ? "账号探测通过" : "Account probe passed") : (locale === "zh" ? "账号探测失败，状态已更新" : "Account probe failed; status updated"), result.success ? "success" : "error");
      await reloadAccounts();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setProbingIDs((current) => {
        const next = new Set(current);
        next.delete(account.id);
        return next;
      });
    }
  }

  async function probeSelected() {
    const ids = [...selected];
    if (!ids.length) return;
    setBatchProbing(true);
    setProbingIDs(new Set(ids));
    try {
      const result = await apiFetch<AccountProbeBatchResult>("/accounts/probe", { method: "POST", ...jsonBody({ ids }) });
      setProbeResult(result);
      toast(locale === "zh" ? `探测完成：${result.succeeded} 通过，${result.failed} 失败` : `Probe complete: ${result.succeeded} passed, ${result.failed} failed`, result.failed ? "error" : "success");
      await reloadAccounts();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setBatchProbing(false);
      setProbingIDs(new Set());
    }
  }

  async function toggleAccount(account: Account) {
    setUpdatingID(account.id);
    const status: AccountStatus = account.status === "disabled" ? "active" : "disabled";
    try {
      await apiFetch<Account>(`/accounts/${account.id}`, { method: "PATCH", ...jsonBody({ status }) });
      toast(status === "active" ? (locale === "zh" ? "账号已启用" : "Account enabled") : (locale === "zh" ? "账号已停用" : "Account disabled"));
      await reloadAccounts();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setUpdatingID("");
    }
  }

  async function updatePolicy(strategy: AccountSchedulingStrategy) {
    setSavingPolicy(true);
    try {
      await apiFetch("/accounts/policy", { method: "PUT", ...jsonBody({ strategy }) });
      toast(locale === "zh" ? "调度策略已更新" : "Scheduling strategy updated");
      await policyResource.reload();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setSavingPolicy(false);
    }
  }

  async function editAccountSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editAccount) return;
    setSaving(true); setFormError("");
    const form = new FormData(event.currentTarget);
    try {
      await apiFetch(`/accounts/${editAccount.id}`, { method: "PATCH", ...jsonBody({
        name: form.get("name"),
        tier: form.get("tier"),
        status: form.get("status"),
        priority: Number(form.get("priority")),
        concurrency_limit: Number(form.get("concurrency_limit")),
      }) });
      setEditAccount(null);
      toast(locale === "zh" ? "账号设置已保存" : "Account updated");
      await reloadAccounts();
    } catch (reason) {
      setFormError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setSaving(false);
    }
  }

  async function batchSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setSaving(true); setFormError("");
    const form = new FormData(event.currentTarget);
    const payload: Record<string, unknown> = { ids: [...selected] };
    if (form.get("apply_status")) payload.status = form.get("status");
    if (form.get("apply_tier")) payload.tier = form.get("tier");
    if (form.get("apply_proxy")) payload.proxy_id = form.get("proxy_id");
    if (form.get("apply_models")) payload.models = splitCommaList(form.get("models"));
    if (form.get("apply_tags")) payload.tags = splitCommaList(form.get("tags"));
    if (form.get("apply_priority")) payload.priority = Number(form.get("priority"));
    if (form.get("apply_concurrency")) payload.concurrency_limit = Number(form.get("concurrency_limit"));
    if (Object.keys(payload).length === 1) {
      setFormError(locale === "zh" ? "至少选择一个要修改的字段。" : "Select at least one field to update.");
      setSaving(false);
      return;
    }
    try {
      await apiFetch("/accounts/batch", { method: "PATCH", ...jsonBody(payload) });
      setBatchOpen(false); setSelected(new Set());
      toast(locale === "zh" ? `已更新 ${selected.size} 个账号` : `Updated ${selected.size} accounts`);
      await reloadAccounts();
    } catch (reason) {
      setFormError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setSaving(false);
    }
  }

  async function beginOAuth() {
    setOAuthLoading(true); setFormError("");
    try {
      const session = await apiFetch<OAuthSession>("/oauth/authorize", { method: "POST", ...jsonBody({}) });
      setOAuthSession(session);
      window.open(session.authorization_url, "_blank", "noopener,noreferrer");
    } catch (reason) { setFormError(reason instanceof Error ? reason.message : t("common.requestFailed")); }
    finally { setOAuthLoading(false); }
  }

  async function exchangeOAuth(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setOAuthLoading(true); setFormError("");
    const form = new FormData(event.currentTarget);
    const callback = String(form.get("callback") ?? "").trim();
    let code = callback; let returnedState = "";
    try {
      const parsed = new URL(callback);
      code = parsed.searchParams.get("code") ?? "";
      returnedState = parsed.searchParams.get("state") ?? "";
    } catch { /* A bare authorization code is accepted. */ }
    try {
      await apiFetch<Account>("/oauth/exchange", { method: "POST", ...jsonBody({ code, state: returnedState || oauthSession?.state, verifier: oauthSession?.verifier, name: form.get("name"), email: form.get("email"), tier: form.get("tier"), priority: Number(form.get("priority")), concurrency_limit: Number(form.get("concurrency_limit")) }) });
      setOAuthOpen(false); setOAuthSession(null);
      toast(locale === "zh" ? "OAuth 账号已添加" : "OAuth account added");
      await reloadAccounts();
    } catch (reason) { setFormError(reason instanceof Error ? reason.message : t("common.requestFailed")); }
    finally { setOAuthLoading(false); }
  }

  async function importAccounts(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setSaving(true); setFormError("");
    const form = new FormData(event.currentTarget);
    form.delete("files");
    for (const file of importFiles) form.append("files", file, file.name);
    try {
      const result = await apiFetch<ImportResult>("/accounts/import", { method: "POST", body: form });
      setImportOpen(false);
      setImportFiles([]);
      toast(locale === "zh" ? `已导入 ${result.imported} 个账号` : `Imported ${result.imported} accounts`);
      await reloadAccounts();
    } catch (reason) { setFormError(reason instanceof Error ? reason.message : t("common.requestFailed")); }
    finally { setSaving(false); }
  }

  async function refreshOAuth(id: string) {
    setRefreshingID(id);
    try {
      await apiFetch<Account>(`/oauth/refresh/${id}`, { method: "POST", ...jsonBody({}) });
      toast(locale === "zh" ? "OAuth 凭据已刷新" : "OAuth credentials refreshed");
      await reloadAccounts();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
      await reloadAccounts();
    }
    finally { setRefreshingID(""); }
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setSaving(true); setFormError("");
    const form = new FormData(event.currentTarget);
    const kind = form.get("kind") as CredentialKind;
    const payload = { name: form.get("name"), kind, email: form.get("email"), access_token: form.get("access_token"), sso: form.get("sso"), sso_rw: form.get("sso_rw"), priority: Number(form.get("priority")), concurrency_limit: Number(form.get("concurrency_limit")), tags: String(form.get("tags") ?? "").split(",").map((tag) => tag.trim()).filter(Boolean) };
    try {
      await apiFetch<Account>("/accounts", { method: "POST", ...jsonBody(payload) });
      setOpen(false); toast(locale === "zh" ? "账号已添加" : "Account added"); await reloadAccounts();
    } catch (reason) { setFormError(reason instanceof Error ? reason.message : t("common.requestFailed")); }
    finally { setSaving(false); }
  }

  return (
    <ContentFrame>
      <PageHeader title={t("account.title")} description={t("account.description")} actions={<><IconButton label={t("common.refresh")} variant="tertiary" onClick={() => void reloadAccounts()} loading={resource.loading || quotaResource.loading}><RefreshCw className="size-4" /></IconButton><Button variant="secondary" size="small" onClick={() => { setFormError(""); setImportFiles([]); setImportOpen(true); }}><Upload className="size-3.5" />{locale === "zh" ? "批量导入" : "Import"}</Button><Button variant="secondary" size="small" onClick={() => { setFormError(""); setOAuthOpen(true); }}><KeyRound className="size-3.5" />OAuth</Button><Button size="small" onClick={() => { setFormError(""); setOpen(true); }}><Plus className="size-3.5" />{locale === "zh" ? "添加账号" : "Add Account"}</Button></>} />
      <QuotaSummaryBand summary={quotaResource.data} locale={locale} />
      <Toolbar search={search} onSearch={(value) => { setSearch(value); setPage(1); }} placeholder={locale === "zh" ? "搜索名称、邮箱或标签" : "Search name, email, or tag"} filter={filter} onFilter={(value) => { setFilter(value); setPage(1); }} filterOptions={[{ value: "all", label: locale === "zh" ? "全部状态" : "All statuses" }, { value: "active", label: "Active" }, { value: "cooldown", label: "Cooldown" }, { value: "expired", label: "Expired" }, { value: "disabled", label: "Disabled" }, { value: "error", label: "Error" }]} filters={<div className="w-full sm:w-44"><Select aria-label={locale === "zh" ? "账号调度策略" : "Account scheduling strategy"} value={policyResource.data.strategy} disabled={savingPolicy} onChange={(event) => void updatePolicy(event.target.value as AccountSchedulingStrategy)} options={[{ value: "affinity", label: locale === "zh" ? "策略：会话亲和" : "Strategy: Affinity" }, { value: "priority", label: locale === "zh" ? "策略：优先级" : "Strategy: Priority" }, { value: "round_robin", label: locale === "zh" ? "策略：轮询" : "Strategy: Round robin" }]} /></div>} trailing={<><Button className="w-full sm:w-auto" size="small" variant="secondary" disabled={selected.size === 0 || batchProbing} loading={batchProbing} onClick={() => void probeSelected()}><Activity className="size-3.5" />{locale === "zh" ? `批量探测 (${selected.size})` : `Probe selected (${selected.size})`}</Button><Button className="w-full sm:w-auto" size="small" variant="secondary" disabled={selected.size === 0 || batchProbing} onClick={() => { setFormError(""); setBatchOpen(true); }}><UsersRound className="size-3.5" />{locale === "zh" ? `批量编辑 (${selected.size})` : `Edit selected (${selected.size})`}</Button></>} />
      {resource.loading && !accountItems.length ? <LoadingState label={t("common.loading")} /> : resource.error ? <ErrorState title={t("common.requestFailed")} description={resource.error.message} onRetry={() => void reloadAccounts()} /> : rows.length ? <DataTable ariaLabel={t("account.title")} data={rows} columns={columns} getRowId={(row) => row.id} /> : <EmptyState title={locale === "zh" ? "没有匹配的账号" : "No matching accounts"} description={locale === "zh" ? "添加上游凭据，或调整当前筛选条件。" : "Add an upstream credential or adjust the current filters."} action={<Button size="small" onClick={() => setOpen(true)}><Plus className="size-3.5" />{locale === "zh" ? "添加账号" : "Add Account"}</Button>} />}
      <PageFooter count={rows.length} noun={locale === "zh" ? "个账号" : "accounts"} total={total} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={(value) => { setPageSize(value); setPage(1); }} locale={locale} />

      <Dialog open={Boolean(editAccount)} onOpenChange={(next) => { if (!next) setEditAccount(null); }}><DialogContent title={locale === "zh" ? "编辑账号" : "Edit Account"} description={editAccount?.email || editAccount?.id}>
        {editAccount ? <form onSubmit={editAccountSubmit}><div className="grid gap-4 px-5 py-5"><Input name="name" defaultValue={editAccount.name} label={t("common.name")} required /><div className="grid gap-4 sm:grid-cols-2"><Input name="tier" defaultValue={editAccount.tier} label={locale === "zh" ? "账号等级" : "Tier"} /><Select name="status" defaultValue={editAccount.status} label={t("common.status")} options={[{ value: "active", label: locale === "zh" ? "已启用" : "Active" }, { value: "disabled", label: locale === "zh" ? "已停用" : "Disabled" }]} /></div><div className="grid gap-4 sm:grid-cols-2"><Input name="priority" type="number" defaultValue={editAccount.priority} label={locale === "zh" ? "优先级" : "Priority"} required /><Input name="concurrency_limit" type="number" min="1" defaultValue={editAccount.concurrency_limit} label={locale === "zh" ? "并发限制" : "Concurrency Limit"} required /></div>{formError ? <p role="alert" className="text-copy-13 text-danger">{formError}</p> : null}</div><FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button type="submit" loading={saving}>{t("common.save")}</Button></FormActions></form> : null}
      </DialogContent></Dialog>

      <Dialog open={batchOpen} onOpenChange={setBatchOpen}><DialogContent title={locale === "zh" ? `批量编辑 ${selected.size} 个账号` : `Edit ${selected.size} accounts`} description={locale === "zh" ? "仅勾选的字段会应用到所选账号。" : "Only checked fields are applied to selected accounts."} className="max-w-xl">
        <form onSubmit={batchSubmit}><div className="grid gap-x-5 px-5 py-2 sm:grid-cols-2"><BatchField checkboxName="apply_status" label={locale === "zh" ? "修改启用状态" : "Change status"}><Select name="status" defaultValue="active" aria-label={locale === "zh" ? "批量状态" : "Batch status"} options={[{ value: "active", label: locale === "zh" ? "启用" : "Enable" }, { value: "disabled", label: locale === "zh" ? "停用" : "Disable" }]} /></BatchField><BatchField checkboxName="apply_tier" label={locale === "zh" ? "修改账号等级" : "Change tier"}><Select name="tier" defaultValue="basic" aria-label={locale === "zh" ? "批量账号等级" : "Batch tier"} options={[{ value: "basic", label: "Basic" }, { value: "super", label: "Super" }, { value: "heavy", label: "Heavy" }]} /></BatchField><BatchField checkboxName="apply_priority" label={locale === "zh" ? "修改优先级" : "Change priority"}><Input name="priority" type="number" defaultValue="100" aria-label={locale === "zh" ? "批量优先级" : "Batch priority"} /></BatchField><BatchField checkboxName="apply_concurrency" label={locale === "zh" ? "修改并发限制" : "Change concurrency"}><Input name="concurrency_limit" type="number" min="1" defaultValue="4" aria-label={locale === "zh" ? "批量并发限制" : "Batch concurrency"} /></BatchField><BatchField checkboxName="apply_proxy" label={locale === "zh" ? "修改绑定代理" : "Change proxy"}><Select name="proxy_id" defaultValue="" aria-label={locale === "zh" ? "批量绑定代理" : "Batch proxy"} options={proxyOptions} /></BatchField><BatchField checkboxName="apply_models" label={locale === "zh" ? "修改可用模型" : "Change models"}><Input name="models" aria-label={locale === "zh" ? "批量可用模型" : "Batch models"} placeholder="grok-4, grok-4-fast" /></BatchField><BatchField checkboxName="apply_tags" label={locale === "zh" ? "修改标签" : "Change tags"}><Input name="tags" aria-label={locale === "zh" ? "批量标签" : "Batch tags"} placeholder="production, primary" /></BatchField>{formError ? <p role="alert" className="py-3 text-copy-13 text-danger sm:col-span-2">{formError}</p> : null}</div><FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button type="submit" loading={saving}>{locale === "zh" ? "应用更改" : "Apply changes"}</Button></FormActions></form>
      </DialogContent></Dialog>

      <Dialog open={Boolean(probeResult)} onOpenChange={(next) => { if (!next) setProbeResult(null); }}><DialogContent title={locale === "zh" ? "账号探测结果" : "Account Probe Results"} description={probeResult ? (locale === "zh" ? `${probeResult.succeeded} 个通过，${probeResult.failed} 个失败` : `${probeResult.succeeded} passed, ${probeResult.failed} failed`) : undefined} className="max-w-xl">
        <div aria-live="polite" className="max-h-[55dvh] divide-y divide-border overflow-y-auto px-5">
          {probeResult?.items.map((item) => <ProbeResultRow key={item.account_id} item={item} fallbackName={accountItems.find((account) => account.id === item.account_id)?.name} locale={locale} />)}
        </div>
        <FormActions><DialogClose asChild><Button type="button">{locale === "zh" ? "完成" : "Done"}</Button></DialogClose></FormActions>
      </DialogContent></Dialog>

      <Dialog open={open} onOpenChange={setOpen}><DialogContent title={locale === "zh" ? "添加账号" : "Add Account"} description={locale === "zh" ? "凭据写入后将加密保存。" : "Credentials are encrypted at rest after submission."}>
        <form onSubmit={submit}><div className="grid gap-4 px-5 py-5"><Input name="name" label={t("common.name")} placeholder="Production account" required /><div className="grid gap-4 sm:grid-cols-2"><Select name="kind" label={locale === "zh" ? "凭据类型" : "Credential Type"} options={[{ value: "cli_oauth", label: "CLI OAuth" }, { value: "console_sso", label: "Console SSO" }, { value: "grok_sso", label: "Grok SSO" }]} /><Input name="email" type="email" label={locale === "zh" ? "邮箱" : "Email"} placeholder="name@example.com" /></div><Textarea name="access_token" label="Access Token" placeholder="Token" rows={3} /><div className="grid gap-4 sm:grid-cols-2"><Input name="sso" type="password" label="SSO" autoComplete="off" /><Input name="sso_rw" type="password" label="SSO-RW" autoComplete="off" /></div><div className="grid gap-4 sm:grid-cols-2"><Input name="priority" type="number" defaultValue="100" label={locale === "zh" ? "优先级" : "Priority"} /><Input name="concurrency_limit" type="number" min="1" defaultValue="4" label={locale === "zh" ? "并发限制" : "Concurrency Limit"} /></div><Input name="tags" label={locale === "zh" ? "标签" : "Tags"} description={locale === "zh" ? "使用英文逗号分隔。" : "Separate tags with commas."} placeholder="production, primary" />{formError ? <p role="alert" className="text-copy-13 text-danger">{formError}</p> : null}</div><FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button type="submit" loading={saving}>{locale === "zh" ? "添加账号" : "Add Account"}</Button></FormActions></form>
      </DialogContent></Dialog>

      <Dialog open={importOpen} onOpenChange={(next) => { setImportOpen(next); if (!next) setImportFiles([]); }}><DialogContent title={locale === "zh" ? "批量导入账号" : "Import Accounts"} description={locale === "zh" ? "支持 TXT、JSON、xAI Build OAuth 以及 grok2api 账号池格式。" : "Accepts TXT, JSON, xAI Build OAuth, and grok2api pool exports."}>
        <form onSubmit={importAccounts}><div className="grid gap-4 px-5 py-5"><ImportFilePicker files={importFiles} locale={locale} onChange={setImportFiles} /><Textarea name="tokens" label={locale === "zh" ? "凭据文本" : "Credential Text"} description={locale === "zh" ? "每行一个凭据；文件和文本可同时提交。" : "One credential per line; file and text can be submitted together."} rows={5} /><div className="grid gap-4 sm:grid-cols-2"><Select name="kind" defaultValue="grok_sso" label={locale === "zh" ? "凭据类型" : "Credential Type"} options={[{ value: "grok_sso", label: "Grok SSO" }, { value: "console_sso", label: "Console SSO" }, { value: "cli_oauth", label: "CLI OAuth Token" }]} /><Select name="tier" defaultValue="basic" label={locale === "zh" ? "账号等级" : "Tier"} options={[{ value: "basic", label: "Basic" }, { value: "super", label: "Super" }, { value: "heavy", label: "Heavy" }]} /></div><Input name="tags" label={locale === "zh" ? "标签" : "Tags"} placeholder="imported, production" />{formError ? <p role="alert" className="text-copy-13 text-danger">{formError}</p> : null}</div><FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button type="submit" loading={saving}><Upload className="size-3.5" />{locale === "zh" ? "开始导入" : "Import"}</Button></FormActions></form>
      </DialogContent></Dialog>

      <Dialog open={oauthOpen} onOpenChange={(next) => { setOAuthOpen(next); if (!next) setOAuthSession(null); }}><DialogContent title={locale === "zh" ? "添加 CLI OAuth 账号" : "Add CLI OAuth Account"} description={locale === "zh" ? "在 xAI 完成授权后粘贴回调 URL 或授权码。" : "Complete xAI authorization, then paste the callback URL or code."}>
        <form onSubmit={exchangeOAuth}><div className="grid gap-4 px-5 py-5"><Input name="name" label={t("common.name")} placeholder="CLI OAuth account" required /><Input name="email" type="email" label={locale === "zh" ? "邮箱" : "Email"} placeholder="name@example.com" /><div className="grid gap-4 sm:grid-cols-2"><Select name="tier" defaultValue="basic" label={locale === "zh" ? "账号等级" : "Tier"} options={[{ value: "basic", label: "Basic" }, { value: "super", label: "Super" }, { value: "heavy", label: "Heavy" }]} /><Input name="concurrency_limit" type="number" min="1" defaultValue="4" label={locale === "zh" ? "并发限制" : "Concurrency Limit"} /></div><Input name="priority" type="number" defaultValue="100" label={locale === "zh" ? "优先级" : "Priority"} />{!oauthSession ? <Button type="button" variant="secondary" onClick={() => void beginOAuth()} loading={oauthLoading}><ExternalLink className="size-3.5" />{locale === "zh" ? "打开 xAI 授权页" : "Open xAI Authorization"}</Button> : <><div className="rounded-[6px] border border-border bg-subtle px-3 py-2 text-copy-13 text-fg-muted">{locale === "zh" ? "授权页已打开；完成授权后返回此处。" : "Authorization opened; return here after approval."}</div><Input name="callback" label={locale === "zh" ? "回调 URL 或授权码" : "Callback URL or Code"} autoComplete="off" required /></>}{formError ? <p role="alert" className="text-copy-13 text-danger">{formError}</p> : null}</div><FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button type="submit" loading={oauthLoading} disabled={!oauthSession}>{locale === "zh" ? "完成绑定" : "Complete"}</Button></FormActions></form>
      </DialogContent></Dialog>
    </ContentFrame>
  );
}

function ImportFilePicker({ files, locale, onChange }: { files: File[]; locale: "zh" | "en"; onChange: (files: File[]) => void }) {
  const inputID = "account-import-files";
  const inputRef = useRef<HTMLInputElement>(null);
  return (
    <div className="grid min-w-0 gap-1.5">
      <span className="text-label-13 font-medium text-fg">{locale === "zh" ? "导入文件" : "Import Files"}</span>
      <div className="rounded-[6px] border border-border bg-subtle/40 p-2.5">
        <div className="flex min-w-0 items-center gap-2">
          <label htmlFor={inputID} className="inline-flex h-8 shrink-0 cursor-pointer items-center gap-2 rounded-[6px] border border-border bg-surface px-2.5 text-label-13 font-medium text-fg shadow-control transition-[border-color,background-color] hover:border-border-strong hover:bg-subtle focus-within:shadow-focus">
            <Upload aria-hidden="true" className="size-3.5" />
            {locale === "zh" ? "选择文件" : "Choose Files"}
            <input
              ref={inputRef}
              id={inputID}
              name="files"
              type="file"
              accept=".txt,.json,text/plain,application/json"
              multiple
              className="sr-only"
              onChange={(event) => onChange(Array.from(event.currentTarget.files ?? []))}
            />
          </label>
          <span className="min-w-0 flex-1 truncate text-copy-13 text-fg-muted" aria-live="polite">
            {files.length > 0
              ? (locale === "zh" ? `已选择 ${files.length} 个文件` : `${files.length} files selected`)
              : (locale === "zh" ? "可一次选择多个 JSON 或 TXT 文件" : "Select multiple JSON or TXT files")}
          </span>
          {files.length > 0 ? <IconButton type="button" label={locale === "zh" ? "清空所选文件" : "Clear selected files"} variant="tertiary" onClick={() => { if (inputRef.current) inputRef.current.value = ""; onChange([]); }}><X className="size-3.5" /></IconButton> : null}
        </div>
        {files.length > 0 ? (
          <ul className="mt-2 max-h-28 divide-y divide-border overflow-y-auto rounded-[5px] border border-border bg-surface px-2.5">
            {files.map((file, index) => <li key={`${file.name}-${file.size}-${index}`} className="flex min-w-0 items-center gap-2 py-1.5 text-copy-13"><FileJson aria-hidden="true" className="size-3.5 shrink-0 text-fg-subtle" /><span className="min-w-0 flex-1 truncate" title={file.name}>{file.name}</span><span className="shrink-0 tabular-nums text-fg-subtle">{formatFileSize(file.size)}</span></li>)}
          </ul>
        ) : null}
      </div>
    </div>
  );
}

function CredentialExpiry({ account, locale }: { account: Account; locale: "zh" | "en" }) {
  if (account.kind !== "cli_oauth") return <span className="text-fg-subtle">-</span>;
  if (!account.credential_expires_at) return <span className="text-copy-13 text-fg-subtle">{locale === "zh" ? "未知" : "Unknown"}</span>;
  const expiry = new Date(account.credential_expires_at);
  if (Number.isNaN(expiry.getTime())) return <span className="text-copy-13 text-fg-subtle">{locale === "zh" ? "未知" : "Unknown"}</span>;
  const expired = account.status === "expired";
  const stateClass = expired ? "text-danger" : account.status === "cooldown" ? "text-amber" : "text-fg-muted";
  const absolute = new Intl.DateTimeFormat(locale === "zh" ? "zh-CN" : "en-US", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(expiry);
  return <div className="grid gap-0.5 whitespace-nowrap"><time dateTime={account.credential_expires_at} title={expiry.toLocaleString()} className="font-mono text-label-13 tabular-nums text-fg">{absolute}</time><span className={`text-[11px] ${stateClass}`}>{expired ? (locale === "zh" ? "已过期" : "Expired") : formatRelative(account.credential_expires_at, locale)}</span></div>;
}

function formatFileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.ceil(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function AccountQuota({ account, locale }: { account: Account; locale: "zh" | "en" }) {
  if (account.quota?.requests_unlimited) return <span className="text-copy-13 text-fg-muted">{locale === "zh" ? "无限" : "Unlimited"}</span>;
  const limit = account.quota?.requests_limit ?? 0;
  if (limit <= 0) return <span className="text-copy-13 text-fg-subtle">{locale === "zh" ? "未知" : "Unknown"}</span>;
  return <QuotaBar value={account.quota?.requests_remaining ?? 0} max={limit} />;
}

function ProbeResultRow({ item, fallbackName, locale }: { item: AccountProbeResult; fallbackName?: string; locale: "zh" | "en" }) {
  return <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-3 py-4">
    {item.success ? <CircleCheck className="mt-0.5 size-4 text-green" /> : <CircleX className="mt-0.5 size-4 text-danger" />}
    <div className="min-w-0">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="truncate text-label-14 font-medium text-fg">{item.account?.name || fallbackName || item.account_id}</span>
        {item.account ? <AccountStatusBadge status={item.account.status} /> : null}
      </div>
      <div className="mt-1 flex flex-wrap gap-x-3 text-[11px] tabular-nums text-fg-subtle"><span>{item.model || "-"}</span><span>HTTP {item.status_code ?? "-"}</span><span>{item.duration_ms.toLocaleString()} ms</span></div>
      <p className={`mt-1 break-words text-copy-13 ${item.success ? "text-fg-muted" : "text-danger"}`}>{item.success ? (locale === "zh" ? "上游返回了可解析的有效响应。" : "Upstream returned a valid parseable response.") : item.message}</p>
    </div>
  </div>;
}

function QuotaSummaryBand({ summary, locale }: { summary: AccountQuotaSummary; locale: "zh" | "en" }) {
  const quota = summary.requests;
  return <section aria-label={locale === "zh" ? "请求额度汇总" : "Request quota summary"} className="grid grid-cols-2 border-t border-border bg-surface xl:grid-cols-4">
    <QuotaMetric className="border-r" label={locale === "zh" ? "可用账号总额度" : "Available quota"} value={quotaValue(quota, "limit", locale)} detail={locale === "zh" ? `${summary.available_accounts} / ${summary.total_accounts} 个账号可用` : `${summary.available_accounts} / ${summary.total_accounts} accounts available`} />
    <QuotaMetric className="xl:border-r" label={locale === "zh" ? "已用" : "Used"} value={quotaValue(quota, "used", locale)} detail={quotaDetail(quota, locale)} />
    <QuotaMetric className="border-r" label={locale === "zh" ? "剩余" : "Remaining"} value={quotaValue(quota, "remaining", locale)} detail={quota.reset_at ? formatRelative(quota.reset_at, locale) : undefined} />
    <QuotaMetric label={locale === "zh" ? "使用率" : "Usage"} value={quota.state === "known" || quota.state === "partial" ? `${(quota.usage_percent ?? 0).toFixed(1)}%` : quotaValue(quota, "usage_percent", locale)} detail={quota.state === "partial" ? (locale === "zh" ? "仅统计已知额度" : "Known quotas only") : undefined} />
  </section>;
}

function QuotaMetric({ label, value, detail, className = "" }: { label: string; value: string; detail?: string; className?: string }) {
  return <div className={`min-w-0 border-b border-border px-4 py-3.5 sm:px-6 xl:border-b-0 lg:px-8 ${className}`}><div className="truncate text-label-13 text-fg-muted">{label}</div><div className="mt-1 truncate text-heading-20 font-semibold tabular-nums text-fg">{value}</div><div className="mt-0.5 min-h-4 truncate text-[11px] text-fg-subtle">{detail ?? " "}</div></div>;
}

function quotaValue(summary: AccountQuotaMetricSummary, field: "limit" | "used" | "remaining" | "usage_percent", locale: "zh" | "en") {
  if (summary.state === "unlimited") return locale === "zh" ? "无限" : "Unlimited";
  if (summary.state === "mixed") return locale === "zh" ? "多窗口" : "Mixed windows";
  if (summary.state === "unknown") return locale === "zh" ? "未知" : "Unknown";
  const value = summary[field];
  if (value == null) return "-";
  const formatted = field === "usage_percent" ? `${value.toFixed(1)}%` : value.toLocaleString();
  return summary.state === "partial" ? `≥ ${formatted}` : formatted;
}

function quotaDetail(summary: AccountQuotaMetricSummary, locale: "zh" | "en") {
  if (summary.state === "mixed") return locale === "zh" ? `${summary.window_count} 个重置窗口` : `${summary.window_count} reset windows`;
  if (summary.unknown_accounts > 0) return locale === "zh" ? `${summary.unknown_accounts} 个账号额度未知` : `${summary.unknown_accounts} unknown accounts`;
  return undefined;
}

function BatchField({ checkboxName, label, children }: { checkboxName: string; label: string; children: React.ReactNode }) {
  return <div className="grid grid-cols-[auto_1fr] items-start gap-3 border-b border-border py-3"><input name={checkboxName} type="checkbox" aria-label={label} className="mt-2.5 size-4 accent-blue-700" /><div className="grid min-w-0 gap-1.5"><span className="text-label-13 font-medium text-fg">{label}</span>{children}</div></div>;
}

function splitCommaList(value: FormDataEntryValue | null) {
  return String(value ?? "").split(",").map((item) => item.trim()).filter(Boolean);
}
