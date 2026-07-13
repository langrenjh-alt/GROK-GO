"use client";

import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { Copy, KeyRound, MoreHorizontal, Pencil, Plus, Power, PowerOff, RefreshCw, Trash2 } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button, IconButton } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { Input } from "@/components/ui/form";
import { EmptyState, ErrorState, LoadingState } from "@/components/ui/feedback";
import { useToast } from "@/components/ui/toast";
import { useI18n } from "@/components/i18n-provider";
import { apiFetch, jsonBody } from "@/lib/api";
import { cn } from "@/lib/cn";
import { formatLimit, formatRelative } from "@/lib/format";
import type { ClientKey, ListResponse } from "@/lib/types";
import { normalizeList } from "@/lib/types";
import { useResource } from "@/lib/use-resource";
import { DEFAULT_PAGE_SIZE, paginatedPath } from "@/lib/pagination";
import { usePageClamp } from "@/lib/use-page-clamp";
import { ContentFrame, FormActions, PageFooter, Toolbar } from "./shared";

export function KeysView() {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE);
  const resource = useResource<ClientKey[] | ListResponse<ClientKey>>(paginatedPath("/keys", page, pageSize), []);
  const [createOpen, setCreateOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState("");
  const [revealedKey, setRevealedKey] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<ClientKey | null>(null);
  const [editTarget, setEditTarget] = useState<ClientKey | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [updating, setUpdating] = useState(false);
  const [busyKeyID, setBusyKeyID] = useState("");

  const unlimitedHint = locale === "zh" ? "0 表示无限" : "Enter 0 for unlimited";
  const keyList = normalizeList(resource.data);
  const keyItems = keyList.items;
  const total = keyList.total ?? keyItems.length;
  usePageClamp(page, pageSize, total, setPage);
  const rows = useMemo(
    () => keyItems.filter((item) =>
      (filter === "all" || String(item.enabled) === filter)
      && `${item.name} ${item.prefix}`.toLowerCase().includes(search.toLowerCase()),
    ),
    [keyItems, search, filter],
  );

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSaving(true);
    setFormError("");
    const data = new FormData(event.currentTarget);
    try {
      const result = await apiFetch<{ key: string }>("/keys", {
        method: "POST",
        ...jsonBody({
          name: data.get("name"),
          rpm: Number(data.get("rpm")),
          concurrency_limit: Number(data.get("concurrency_limit")),
          daily_request_limit: Number(data.get("daily_request_limit")),
          monthly_token_limit: Number(data.get("monthly_token_limit")),
          expires_at: data.get("expires_at") || undefined,
        }),
      });
      setCreateOpen(false);
      setRevealedKey(result.key);
      await resource.reload();
    } catch (reason) {
      setFormError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setSaving(false);
    }
  }

  async function copySecret(key: ClientKey) {
    setBusyKeyID(key.id);
    try {
      const result = await apiFetch<{ key: string }>(`/keys/${key.id}/reveal`, { method: "POST" });
      await navigator.clipboard.writeText(result.key);
      toast(t("common.copied"));
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setBusyKeyID("");
    }
  }

  async function deleteKey() {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await apiFetch(`/keys/${deleteTarget.id}`, { method: "DELETE" });
      toast(locale === "zh" ? "访问密钥已删除" : "API key deleted");
      setDeleteTarget(null);
      await resource.reload();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setDeleting(false);
    }
  }

  async function updateKey(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editTarget) return;
    setUpdating(true);
    setFormError("");
    const data = new FormData(event.currentTarget);
    const expiration = String(data.get("expires_at") ?? "").trim();
    try {
      await apiFetch(`/keys/${editTarget.id}`, {
        method: "PATCH",
        ...jsonBody({
          name: data.get("name"),
          rpm: Number(data.get("rpm")),
          concurrency_limit: Number(data.get("concurrency_limit")),
          daily_request_limit: Number(data.get("daily_request_limit")),
          monthly_token_limit: Number(data.get("monthly_token_limit")),
          expires_at: expiration ? new Date(expiration).toISOString() : null,
        }),
      });
      toast(locale === "zh" ? "访问密钥已更新" : "API key updated");
      setEditTarget(null);
      await resource.reload();
    } catch (reason) {
      setFormError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setUpdating(false);
    }
  }

  async function toggleKey(key: ClientKey) {
    setBusyKeyID(key.id);
    try {
      await apiFetch(`/keys/${key.id}`, { method: "PATCH", ...jsonBody({ enabled: !key.enabled }) });
      toast(locale === "zh" ? `访问密钥已${key.enabled ? "停用" : "启用"}` : `API key ${key.enabled ? "disabled" : "enabled"}`);
      await resource.reload();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setBusyKeyID("");
    }
  }

  async function copyCreatedKey() {
    await navigator.clipboard.writeText(revealedKey);
    toast(t("common.copied"));
  }

  const itemClass = "control-text-sm flex h-8 w-full cursor-pointer items-center gap-2 rounded-[5px] px-2 text-left text-fg outline-none data-[disabled]:pointer-events-none data-[disabled]:opacity-45 data-[highlighted]:bg-subtle";
  const columns: ColumnDef<ClientKey>[] = [
    {
      accessorKey: "name",
      header: t("common.name"),
      cell: ({ row }) => (
        <div className="grid gap-0.5">
          <span className="font-medium">{row.original.name}</span>
          <code className="text-label-13 text-fg-subtle">{row.original.prefix}••••••••</code>
        </div>
      ),
    },
    { accessorKey: "enabled", header: t("common.status"), cell: ({ row }) => <Badge tone={row.original.enabled ? "green" : "neutral"} dot>{row.original.enabled ? t("common.enabled") : t("common.disabled")}</Badge> },
    { id: "rate_limits", header: locale === "zh" ? "速率限制" : "Rate Limits", cell: ({ row }) => <div className="grid gap-0.5 font-mono text-label-13 tabular-nums"><span>RPM {formatLimit(row.original.rpm, locale)}</span><span className="text-fg-subtle">{locale === "zh" ? "并发" : "Concurrency"} {formatLimit(row.original.concurrency_limit, locale)}</span></div> },
    { id: "usage_limits", header: locale === "zh" ? "用量上限" : "Usage Limits", cell: ({ row }) => <div className="grid gap-0.5 font-mono text-label-13 tabular-nums"><span>{locale === "zh" ? "日" : "Day"} {formatLimit(row.original.daily_request_limit, locale)}</span><span className="text-fg-subtle">{locale === "zh" ? "月" : "Month"} {formatLimit(row.original.monthly_token_limit, locale)}</span></div> },
    { accessorKey: "last_used_at", header: locale === "zh" ? "最近使用" : "Last Used", cell: ({ row }) => <span className="whitespace-nowrap text-copy-13 text-fg-muted">{formatRelative(row.original.last_used_at, locale)}</span> },
    {
      id: "actions",
      enableSorting: false,
      header: () => <span className="sr-only">{t("common.actions")}</span>,
      cell: ({ row }) => <div className="flex items-center justify-end gap-0.5">
        <IconButton label={locale === "zh" ? "复制密钥" : "Copy key"} variant="tertiary" loading={busyKeyID === row.original.id} disabled={!row.original.secret_available} onClick={() => void copySecret(row.original)}><Copy className="size-4" /></IconButton>
        <IconButton label={row.original.enabled ? (locale === "zh" ? "停用密钥" : "Disable key") : (locale === "zh" ? "启用密钥" : "Enable key")} variant="tertiary" disabled={busyKeyID === row.original.id} onClick={() => void toggleKey(row.original)}>{row.original.enabled ? <PowerOff className="size-4" /> : <Power className="size-4" />}</IconButton>
        <DropdownMenu.Root>
          <DropdownMenu.Trigger asChild><IconButton label={t("common.actions")} variant="tertiary" disabled={busyKeyID === row.original.id}><MoreHorizontal className="size-4" /></IconButton></DropdownMenu.Trigger>
          <DropdownMenu.Portal><DropdownMenu.Content align="end" sideOffset={5} className="z-[70] w-40 rounded-[7px] border border-border bg-surface p-1 shadow-menu"><DropdownMenu.Item onSelect={() => { setFormError(""); setEditTarget(row.original); }} className={itemClass}><Pencil className="size-3.5" />{locale === "zh" ? "编辑" : "Edit"}</DropdownMenu.Item><DropdownMenu.Separator className="my-1 h-px bg-border" /><DropdownMenu.Item onSelect={() => setDeleteTarget(row.original)} className={cn(itemClass, "text-danger")}><Trash2 className="size-3.5" />{t("common.delete")}</DropdownMenu.Item></DropdownMenu.Content></DropdownMenu.Portal>
        </DropdownMenu.Root>
      </div>,
    },
  ];

  return (
    <ContentFrame>
      <PageHeader
        title={t("key.title")}
        description={t("key.description")}
        actions={(
          <>
            <Button size="small" variant="secondary" onClick={() => void resource.reload()} loading={resource.loading}><RefreshCw className="size-3.5" />{t("common.refresh")}</Button>
            <Button size="small" onClick={() => { setFormError(""); setCreateOpen(true); }}><Plus className="size-3.5" />{locale === "zh" ? "签发密钥" : "Create Key"}</Button>
          </>
        )}
      />
      <KeySummaryBand items={keyItems} locale={locale} />
      <Toolbar search={search} onSearch={(value) => { setSearch(value); setPage(1); }} placeholder={locale === "zh" ? "搜索名称或前缀" : "Search name or prefix"} filter={filter} onFilter={(value) => { setFilter(value); setPage(1); }} filterOptions={[{ value: "all", label: locale === "zh" ? "全部状态" : "All statuses" }, { value: "true", label: t("common.enabled") }, { value: "false", label: t("common.disabled") }]} trailing={<span className="text-copy-13 tabular-nums text-fg-subtle">{rows.length} / {keyItems.length}</span>} />
      {resource.loading && !keyItems.length ? <LoadingState label={t("common.loading")} /> : resource.error ? <ErrorState title={t("common.requestFailed")} description={resource.error.message} onRetry={() => void resource.reload()} /> : rows.length ? <DataTable ariaLabel={t("key.title")} data={rows} columns={columns} getRowId={(row) => row.id} /> : <EmptyState title={locale === "zh" ? "尚未签发密钥" : "No API keys"} description={locale === "zh" ? "创建密钥后，客户端即可调用公开端点。" : "Create a key to let clients call public endpoints."} action={<Button size="small" onClick={() => { setFormError(""); setCreateOpen(true); }}><KeyRound className="size-3.5" />{locale === "zh" ? "签发密钥" : "Create Key"}</Button>} />}
      <PageFooter count={keyItems.length} noun={locale === "zh" ? "个密钥" : "keys"} total={total} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={(value) => { setPageSize(value); setPage(1); }} locale={locale} />

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent title={locale === "zh" ? "签发访问密钥" : "Create API Key"} description={locale === "zh" ? "默认不限制用量，也可设置独立限额。" : "Usage is unlimited by default; optional limits are supported."}>
          <form onSubmit={submit}>
            <div className="grid gap-4 px-5 py-5">
              <Input name="name" label={t("common.name")} placeholder="Production client" required />
              <div className="grid gap-4 sm:grid-cols-2">
                <Input name="rpm" type="number" min="0" defaultValue="0" label="RPM" description={unlimitedHint} required />
                <Input name="concurrency_limit" type="number" min="0" defaultValue="0" label={locale === "zh" ? "并发限制" : "Concurrency Limit"} description={unlimitedHint} required />
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <Input name="daily_request_limit" type="number" min="0" defaultValue="0" label={locale === "zh" ? "每日请求上限" : "Daily Request Limit"} description={unlimitedHint} />
                <Input name="monthly_token_limit" type="number" min="0" defaultValue="0" label={locale === "zh" ? "每月 Token 上限" : "Monthly Token Limit"} description={unlimitedHint} />
              </div>
              <Input name="expires_at" type="datetime-local" label={locale === "zh" ? "过期时间" : "Expiration"} />
              {formError ? <p role="alert" className="text-copy-13 text-danger">{formError}</p> : null}
            </div>
            <FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button type="submit" loading={saving}>{locale === "zh" ? "签发密钥" : "Create Key"}</Button></FormActions>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(revealedKey)} onOpenChange={(value) => { if (!value) setRevealedKey(""); }}>
        <DialogContent title={locale === "zh" ? "访问密钥已创建" : "API Key Created"} description={locale === "zh" ? "可立即复制，也可稍后从操作菜单再次复制。" : "Copy it now or retrieve it later from the actions menu."}>
          <div className="grid gap-4 px-5 py-5">
            <div className="flex items-center gap-2 rounded-[6px] border border-border bg-subtle p-3">
              <code className="min-w-0 flex-1 break-all text-label-13">{revealedKey}</code>
              <IconButton label={locale === "zh" ? "复制密钥" : "Copy key"} variant="secondary" onClick={() => void copyCreatedKey()}><Copy className="size-4" /></IconButton>
            </div>
          </div>
          <FormActions><Button onClick={() => setRevealedKey("")}>{locale === "zh" ? "完成" : "Done"}</Button></FormActions>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(deleteTarget)} onOpenChange={(value) => { if (!value && !deleting) setDeleteTarget(null); }}>
        <DialogContent title={locale === "zh" ? "删除访问密钥" : "Delete API Key"} description={deleteTarget ? `${deleteTarget.name} · ${deleteTarget.prefix}` : undefined}>
          <div className="px-5 py-5 text-copy-14 text-fg-muted">
            {locale === "zh" ? "删除后，使用此密钥的请求会立即失去访问权限。" : "Requests using this key will lose access immediately."}
          </div>
          <FormActions><Button type="button" variant="secondary" disabled={deleting} onClick={() => setDeleteTarget(null)}>{t("common.cancel")}</Button><Button type="button" variant="danger" loading={deleting} onClick={() => void deleteKey()}><Trash2 className="size-4" />{t("common.delete")}</Button></FormActions>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(editTarget)} onOpenChange={(value) => { if (!value && !updating) setEditTarget(null); }}>
        <DialogContent title={locale === "zh" ? "编辑访问密钥" : "Edit API Key"} description={editTarget?.prefix}>
          {editTarget ? (
            <form onSubmit={updateKey}>
              <div className="grid gap-4 px-5 py-5">
                <Input name="name" label={t("common.name")} defaultValue={editTarget.name} required />
                <div className="grid gap-4 sm:grid-cols-2">
                  <Input name="rpm" type="number" min="0" defaultValue={editTarget.rpm} label="RPM" description={unlimitedHint} required />
                  <Input name="concurrency_limit" type="number" min="0" defaultValue={editTarget.concurrency_limit} label={locale === "zh" ? "并发限制" : "Concurrency Limit"} description={unlimitedHint} required />
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Input name="daily_request_limit" type="number" min="0" defaultValue={editTarget.daily_request_limit} label={locale === "zh" ? "每日请求上限" : "Daily Request Limit"} description={unlimitedHint} />
                  <Input name="monthly_token_limit" type="number" min="0" defaultValue={editTarget.monthly_token_limit} label={locale === "zh" ? "每月 Token 上限" : "Monthly Token Limit"} description={unlimitedHint} />
                </div>
                <Input name="expires_at" type="datetime-local" defaultValue={toDateTimeLocal(editTarget.expires_at)} label={locale === "zh" ? "过期时间" : "Expiration"} />
                {formError ? <p role="alert" className="text-copy-13 text-danger">{formError}</p> : null}
              </div>
              <FormActions><Button type="button" variant="secondary" disabled={updating} onClick={() => setEditTarget(null)}>{t("common.cancel")}</Button><Button type="submit" loading={updating}>{t("common.save")}</Button></FormActions>
            </form>
          ) : null}
        </DialogContent>
      </Dialog>
    </ContentFrame>
  );
}

function KeySummaryBand({ items, locale }: { items: ClientKey[]; locale: "zh" | "en" }) {
  const enabled = items.filter((item) => item.enabled).length;
  const unlimited = items.filter((item) => item.rpm === 0 && item.concurrency_limit === 0 && item.daily_request_limit === 0 && item.monthly_token_limit === 0).length;
  const expiring = items.filter((item) => Boolean(item.expires_at)).length;
  return (
    <section aria-label={locale === "zh" ? "密钥汇总" : "API key summary"} className="grid grid-cols-3 border-t border-border bg-surface">
      <KeyMetric label={locale === "zh" ? "已启用" : "Enabled"} value={enabled} className="border-r" />
      <KeyMetric label={locale === "zh" ? "无限配额" : "Unlimited"} value={unlimited} className="border-r" />
      <KeyMetric label={locale === "zh" ? "设定到期" : "With Expiry"} value={expiring} />
    </section>
  );
}

function KeyMetric({ label, value, className = "" }: { label: string; value: number; className?: string }) {
  return <div className={`min-w-0 border-b border-border px-4 py-3 sm:px-6 lg:px-8 ${className}`}><div className="truncate text-label-13 text-fg-muted">{label}</div><div className="mt-1 text-heading-20 font-semibold tabular-nums text-fg">{value.toLocaleString()}</div></div>;
}

function toDateTimeLocal(value?: string) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}
