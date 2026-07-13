"use client";

import { useCallback, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Pencil, Plus, Power, PowerOff, RefreshCw, Stethoscope, Trash2 } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Button, IconButton } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { Input, Switch } from "@/components/ui/form";
import { EmptyState, ErrorState, LoadingState } from "@/components/ui/feedback";
import { useToast } from "@/components/ui/toast";
import { useI18n } from "@/components/i18n-provider";
import { apiFetch, jsonBody } from "@/lib/api";
import { formatRelative } from "@/lib/format";
import type { ListResponse, ProxyRecord } from "@/lib/types";
import { normalizeList } from "@/lib/types";
import { useResource } from "@/lib/use-resource";
import { DEFAULT_PAGE_SIZE, paginatedPath } from "@/lib/pagination";
import { usePageClamp } from "@/lib/use-page-clamp";
import { ContentFrame, FormActions, HealthBadge, PageFooter, Toolbar } from "./shared";

export function ProxiesView() {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE);
  const resource = useResource<ProxyRecord[] | ListResponse<ProxyRecord>>(paginatedPath("/proxies", page, pageSize), []);
  const reload = resource.reload;
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<ProxyRecord | null>(null);
  const [deleting, setDeleting] = useState<ProxyRecord | null>(null);
  const [pending, setPending] = useState<string | null>(null);

  const rows = useMemo(
    () => normalizeList(resource.data).items.filter((item) => (
      (filter === "all" || (filter === "healthy" ? item.healthy && item.enabled : filter === "disabled" ? !item.enabled : !item.healthy && item.enabled))
      && `${item.name} ${item.id}`.toLowerCase().includes(search.toLowerCase())
    )),
    [resource.data, filter, search],
  );
  const proxyList = normalizeList(resource.data);
  const total = proxyList.total ?? proxyList.items.length;
  usePageClamp(page, pageSize, total, setPage);

  const check = useCallback(async (id: string) => {
    setPending(id);
    try {
      await apiFetch(`/proxies/${id}/check`, { method: "POST" });
      toast(locale === "zh" ? "健康检查已完成" : "Health check completed");
      await reload();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setPending(null);
    }
  }, [locale, reload, t, toast]);

  const toggle = useCallback(async (proxy: ProxyRecord) => {
    setPending(proxy.id);
    try {
      await apiFetch(`/proxies/${proxy.id}`, { method: "PATCH", ...jsonBody({ enabled: !proxy.enabled }) });
      toast(locale === "zh" ? `代理已${proxy.enabled ? "停用" : "启用"}` : `Proxy ${proxy.enabled ? "disabled" : "enabled"}`);
      await reload();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setPending(null);
    }
  }, [locale, reload, t, toast]);

  const columns = useMemo<ColumnDef<ProxyRecord>[]>(() => [
    { accessorKey: "name", header: t("common.name"), cell: ({ row }) => <div className="grid gap-0.5"><span className="font-medium">{row.original.name}</span><code className="text-label-13 text-fg-subtle">{row.original.id}</code></div> },
    { id: "health", header: t("common.status"), cell: ({ row }) => <HealthBadge healthy={row.original.healthy} enabled={row.original.enabled} /> },
    { accessorKey: "last_checked_at", header: locale === "zh" ? "最近检查" : "Last Checked", cell: ({ row }) => <span className="whitespace-nowrap text-copy-13 text-fg-muted">{formatRelative(row.original.last_checked_at, locale)}</span> },
    { accessorKey: "last_error", header: locale === "zh" ? "最近错误" : "Last Error", cell: ({ row }) => <span className="block max-w-xs truncate text-copy-13 text-fg-muted" title={row.original.last_error}>{row.original.last_error || "-"}</span> },
    {
      id: "actions",
      enableSorting: false,
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: ({ row }) => (
        <div className="flex justify-end gap-0.5">
          <IconButton label={locale === "zh" ? "检查代理" : "Check proxy"} variant="tertiary" loading={pending === row.original.id} onClick={() => void check(row.original.id)}><Stethoscope className="size-4" /></IconButton>
          <IconButton label={row.original.enabled ? (locale === "zh" ? "停用代理" : "Disable proxy") : (locale === "zh" ? "启用代理" : "Enable proxy")} variant="tertiary" disabled={pending === row.original.id} onClick={() => void toggle(row.original)}>{row.original.enabled ? <PowerOff className="size-4" /> : <Power className="size-4" />}</IconButton>
          <IconButton label={locale === "zh" ? "编辑代理" : "Edit proxy"} variant="tertiary" onClick={() => { setEditing(row.original); setEditorOpen(true); }}><Pencil className="size-4" /></IconButton>
          <IconButton label={locale === "zh" ? "删除代理" : "Delete proxy"} variant="tertiary" onClick={() => setDeleting(row.original)}><Trash2 className="size-4 text-danger" /></IconButton>
        </div>
      ),
    },
  ], [check, locale, pending, t, toggle]);

  return (
    <ContentFrame>
      <PageHeader title={t("proxy.title")} description={t("proxy.description")} actions={<><Button size="small" variant="secondary" loading={resource.loading} onClick={() => void resource.reload()}><RefreshCw className="size-3.5" />{t("common.refresh")}</Button><Button size="small" onClick={() => { setEditing(null); setEditorOpen(true); }}><Plus className="size-3.5" />{locale === "zh" ? "添加代理" : "Add Proxy"}</Button></>} />
      <Toolbar
        search={search}
        onSearch={(value) => { setSearch(value); setPage(1); }}
        placeholder={locale === "zh" ? "搜索代理名称或 ID" : "Search proxy name or ID"}
        filter={filter}
        onFilter={(value) => { setFilter(value); setPage(1); }}
        filterOptions={[
          { value: "all", label: locale === "zh" ? "全部状态" : "All statuses" },
          { value: "healthy", label: locale === "zh" ? "健康" : "Healthy" },
          { value: "unhealthy", label: locale === "zh" ? "异常" : "Unhealthy" },
          { value: "disabled", label: locale === "zh" ? "已停用" : "Disabled" },
        ]}
        trailing={<span className="text-copy-13 tabular-nums text-fg-subtle">{rows.length} / {proxyList.items.length}</span>}
      />
      {resource.loading && !normalizeList(resource.data).items.length ? <LoadingState label={t("common.loading")} /> : resource.error ? <ErrorState title={t("common.requestFailed")} description={resource.error.message} onRetry={() => void resource.reload()} /> : rows.length ? <DataTable ariaLabel={t("proxy.title")} data={rows} columns={columns} getRowId={(row) => row.id} /> : <EmptyState title={locale === "zh" ? "没有代理节点" : "No proxy nodes"} description={locale === "zh" ? "添加 HTTP、HTTPS 或 SOCKS5 出口代理。" : "Add an HTTP, HTTPS, or SOCKS5 egress proxy."} action={<Button size="small" onClick={() => { setEditing(null); setEditorOpen(true); }}><Plus className="size-3.5" />{locale === "zh" ? "添加代理" : "Add Proxy"}</Button>} />}
      <PageFooter count={proxyList.items.length} noun={locale === "zh" ? "个代理" : "proxies"} total={total} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={(value) => { setPageSize(value); setPage(1); }} locale={locale} />

      <Dialog open={editorOpen} onOpenChange={setEditorOpen}>
        {editorOpen ? <ProxyEditor proxy={editing} onSaved={async () => { setEditorOpen(false); await reload(); }} /> : null}
      </Dialog>

      <Dialog open={Boolean(deleting)} onOpenChange={(open) => { if (!open) setDeleting(null); }}>
        {deleting ? <DeleteProxyDialog proxy={deleting} onDeleted={async () => { setDeleting(null); await reload(); }} /> : null}
      </Dialog>
    </ContentFrame>
  );
}

function ProxyEditor({ proxy, onSaved }: { proxy: ProxyRecord | null; onSaved: () => Promise<void> }) {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [enabled, setEnabled] = useState(proxy?.enabled ?? true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const editing = Boolean(proxy);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSaving(true);
    setError("");
    const data = new FormData(event.currentTarget);
    const url = String(data.get("url") ?? "").trim();
    try {
      await apiFetch(editing ? `/proxies/${proxy?.id}` : "/proxies", {
        method: editing ? "PATCH" : "POST",
        ...jsonBody({ name: data.get("name"), ...(url ? { url } : {}), enabled }),
      });
      toast(editing ? (locale === "zh" ? "代理配置已保存" : "Proxy configuration saved") : (locale === "zh" ? "代理已添加" : "Proxy added"));
      await onSaved();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setSaving(false);
    }
  }

  return (
    <DialogContent title={editing ? (locale === "zh" ? "编辑代理" : "Edit Proxy") : (locale === "zh" ? "添加代理" : "Add Proxy")} description={editing ? proxy?.id : (locale === "zh" ? "认证信息将在服务端加密保存。" : "Authentication details are encrypted at rest.")}>
      <form onSubmit={submit}>
        <div className="grid gap-4 px-5 py-5">
          <Input name="name" label={t("common.name")} placeholder="Tokyo egress" defaultValue={proxy?.name ?? ""} required />
          <Input name="url" type="url" label={locale === "zh" ? "代理 URL" : "Proxy URL"} placeholder="http://user:password@host:port" description={editing ? (locale === "zh" ? "留空保留当前 URL；支持 HTTP、HTTPS、SOCKS5。" : "Leave blank to keep the current URL; supports HTTP, HTTPS, and SOCKS5.") : "HTTP, HTTPS, or SOCKS5"} required={!editing} />
          <Switch checked={enabled} onCheckedChange={setEnabled} label={locale === "zh" ? "启用代理" : "Enable Proxy"} description={locale === "zh" ? "启用后可分配给上游账号。" : "Enabled proxies can be assigned to upstream accounts."} />
          {error ? <p role="alert" className="text-copy-13 text-danger">{error}</p> : null}
        </div>
        <FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button type="submit" loading={saving}>{editing ? t("common.save") : (locale === "zh" ? "添加代理" : "Add Proxy")}</Button></FormActions>
      </form>
    </DialogContent>
  );
}

function DeleteProxyDialog({ proxy, onDeleted }: { proxy: ProxyRecord; onDeleted: () => Promise<void> }) {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState("");

  async function remove() {
    setDeleting(true);
    setError("");
    try {
      await apiFetch(`/proxies/${proxy.id}`, { method: "DELETE" });
      toast(locale === "zh" ? "代理已删除" : "Proxy deleted");
      await onDeleted();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setDeleting(false);
    }
  }

  return (
    <DialogContent title={locale === "zh" ? "删除代理" : "Delete Proxy"} description={proxy.name}>
      <div className="px-5 py-5"><p className="text-copy-14 text-fg-muted">{locale === "zh" ? "删除后，使用该节点的账号将恢复为无固定代理。此操作不可撤销。" : "Accounts assigned to this node will return to no fixed proxy. This action is permanent."}</p>{error ? <p role="alert" className="mt-3 text-copy-13 text-danger">{error}</p> : null}</div>
      <FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button variant="danger" loading={deleting} onClick={() => void remove()}><Trash2 className="size-4" />{t("common.delete")}</Button></FormActions>
    </DialogContent>
  );
}
