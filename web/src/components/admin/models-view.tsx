"use client";

import { useCallback, useDeferredValue, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button, IconButton } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { Input, Select, Switch } from "@/components/ui/form";
import { EmptyState, ErrorState, LoadingState } from "@/components/ui/feedback";
import { useToast } from "@/components/ui/toast";
import { useI18n } from "@/components/i18n-provider";
import { apiFetch, jsonBody } from "@/lib/api";
import type { CredentialKind, ListResponse, ModelSpec } from "@/lib/types";
import { normalizeList } from "@/lib/types";
import { useResource } from "@/lib/use-resource";
import { DEFAULT_PAGE_SIZE, paginatedPath } from "@/lib/pagination";
import { usePageClamp } from "@/lib/use-page-clamp";
import { ContentFrame, FormActions, PageFooter, Toolbar } from "./shared";

const capabilityTone: Record<ModelSpec["capability"], "blue" | "green" | "amber" | "neutral" | "red"> = {
  chat: "blue",
  responses: "green",
  messages: "neutral",
  image: "amber",
  image_edit: "amber",
  video: "red",
};

const credentialKinds: CredentialKind[] = ["cli_oauth", "console_sso", "grok_sso"];

function capabilityLabel(value: ModelSpec["capability"], locale: "zh" | "en") {
  const labels: Record<ModelSpec["capability"], [string, string]> = {
    chat: ["聊天", "Chat"],
    responses: ["Responses", "Responses"],
    messages: ["Messages", "Messages"],
    image: ["图片", "Image"],
    image_edit: ["图片编辑", "Image Edit"],
    video: ["视频", "Video"],
  };
  return labels[value][locale === "zh" ? 0 : 1];
}

function credentialLabel(value: CredentialKind, locale: "zh" | "en") {
  const labels: Record<CredentialKind, [string, string]> = {
    cli_oauth: ["CLI OAuth", "CLI OAuth"],
    console_sso: ["Console SSO", "Console SSO"],
    grok_sso: ["grok.com SSO", "grok.com SSO"],
  };
  return labels[value][locale === "zh" ? 0 : 1];
}

export function ModelsView() {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE);
  const deferredSearch = useDeferredValue(search);
  const resource = useResource<ModelSpec[] | ListResponse<ModelSpec>>(paginatedPath("/models", page, pageSize, { q: deferredSearch, capability: filter === "all" ? "" : filter }), []);
  const reload = resource.reload;
  const [pending, setPending] = useState<string | null>(null);
  const [editing, setEditing] = useState<ModelSpec | null>(null);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<ModelSpec | null>(null);
  const [deletePending, setDeletePending] = useState(false);

  const modelList = normalizeList(resource.data);
  const rows = modelList.items;
  const total = modelList.total ?? rows.length;
  usePageClamp(page, pageSize, total, setPage);

  const toggle = useCallback(async (model: ModelSpec, enabled: boolean) => {
    setPending(model.id);
    try {
      await apiFetch(`/models/${model.id}`, { method: "PATCH", ...jsonBody({ enabled }) });
      toast(locale === "zh" ? `模型已${enabled ? "启用" : "停用"}` : `Model ${enabled ? "enabled" : "disabled"}`);
      await reload();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setPending(null);
    }
  }, [locale, reload, t, toast]);

  const columns = useMemo<ColumnDef<ModelSpec>[]>(() => [
    {
      accessorKey: "display_name",
      header: locale === "zh" ? "公开模型" : "Public Model",
      cell: ({ row }) => <div className="grid gap-0.5"><div className="flex min-w-0 flex-wrap items-center gap-2"><span className="font-medium">{row.original.display_name}</span><Badge tone={row.original.catalog_managed ? "blue" : "neutral"}>{row.original.catalog_managed ? (locale === "zh" ? "系统预设" : "Catalog Preset") : (locale === "zh" ? "自定义" : "Custom")}</Badge></div><code className="text-label-13 text-fg-subtle">{row.original.id}</code></div>,
    },
    { accessorKey: "upstream_model", header: locale === "zh" ? "上游映射" : "Upstream Mapping", cell: ({ row }) => <code className="text-label-13">{row.original.upstream_model}</code> },
    { accessorKey: "capability", header: locale === "zh" ? "能力" : "Capability", cell: ({ row }) => <Badge tone={capabilityTone[row.original.capability]}>{capabilityLabel(row.original.capability, locale)}</Badge> },
    { id: "credentials", header: locale === "zh" ? "上游通道" : "Upstream Route", cell: ({ row }) => <div className="flex max-w-xs flex-wrap gap-1">{row.original.credential_kinds.map((kind) => <Badge key={kind}>{credentialLabel(kind, locale)}</Badge>)}</div> },
    { id: "aliases", header: locale === "zh" ? "别名" : "Aliases", cell: ({ row }) => <span className="block max-w-xs truncate text-copy-13 text-fg-muted" title={row.original.aliases?.join(", ")}>{row.original.aliases?.join(", ") || "-"}</span> },
    { accessorKey: "enabled", header: t("common.status"), enableSorting: false, cell: ({ row }) => <Switch label={row.original.enabled ? t("common.enabled") : t("common.disabled")} checked={row.original.enabled} disabled={pending === row.original.id} onCheckedChange={(checked) => void toggle(row.original, checked)} /> },
    { id: "actions", enableSorting: false, header: () => <span className="sr-only">{t("common.actions")}</span>, cell: ({ row }) => <div className="flex items-center justify-end gap-1"><IconButton label={locale === "zh" ? "编辑模型" : "Edit model"} variant="tertiary" onClick={() => setEditing(row.original)}><Pencil className="size-4" /></IconButton>{!row.original.catalog_managed ? <IconButton label={locale === "zh" ? "删除模型" : "Delete model"} variant="tertiary" className="text-danger" onClick={() => setDeleting(row.original)}><Trash2 className="size-4" /></IconButton> : null}</div> },
  ], [locale, pending, t, toggle]);

  async function removeModel() {
    if (!deleting) return;
    setDeletePending(true);
    try {
      await apiFetch(`/models/${deleting.id}`, { method: "DELETE" });
      toast(locale === "zh" ? "自定义模型已删除" : "Custom model deleted");
      setDeleting(null);
      await reload();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setDeletePending(false);
    }
  }

  return (
    <ContentFrame>
      <PageHeader title={t("model.title")} description={t("model.description")} actions={<><Button size="small" variant="secondary" onClick={() => void resource.reload()} loading={resource.loading}><RefreshCw className="size-3.5" />{t("common.refresh")}</Button><Button size="small" onClick={() => setCreating(true)}><Plus className="size-3.5" />{locale === "zh" ? "添加模型" : "Add Model"}</Button></>} />
      <Toolbar
        search={search}
        onSearch={(value) => { setSearch(value); setPage(1); }}
        placeholder={locale === "zh" ? "搜索模型、映射或别名" : "Search model, mapping, or alias"}
        filter={filter}
        onFilter={(value) => { setFilter(value); setPage(1); }}
        filterOptions={[
          { value: "all", label: locale === "zh" ? "全部能力" : "All capabilities" },
          { value: "chat", label: "Chat" },
          { value: "responses", label: "Responses" },
          { value: "messages", label: "Messages" },
          { value: "image", label: "Image" },
          { value: "image_edit", label: locale === "zh" ? "图片编辑" : "Image Edit" },
          { value: "video", label: "Video" },
        ]}
        trailing={<span className="text-copy-13 tabular-nums text-fg-subtle">{rows.length} / {total}</span>}
      />
      {resource.loading && !normalizeList(resource.data).items.length ? <LoadingState label={t("common.loading")} /> : resource.error ? <ErrorState title={t("common.requestFailed")} description={resource.error.message} onRetry={() => void resource.reload()} /> : rows.length ? <DataTable ariaLabel={t("model.title")} data={rows} columns={columns} getRowId={(row) => row.id} /> : <EmptyState title={locale === "zh" ? "没有匹配的模型" : "No matching models"} description={locale === "zh" ? "调整搜索或能力筛选条件。" : "Adjust the search or capability filter."} />}
      <PageFooter count={rows.length} noun={locale === "zh" ? "个模型" : "models"} total={total} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={(value) => { setPageSize(value); setPage(1); }} locale={locale} />
      <Dialog open={Boolean(editing)} onOpenChange={(open) => { if (!open) setEditing(null); }}>
        {editing ? <ModelEditor model={editing} onSaved={async () => { setEditing(null); await reload(); }} /> : null}
      </Dialog>
      <Dialog open={creating} onOpenChange={setCreating}>
        {creating ? <ModelEditor onSaved={async () => { setCreating(false); await reload(); }} /> : null}
      </Dialog>
      <Dialog open={Boolean(deleting)} onOpenChange={(open) => { if (!open && !deletePending) setDeleting(null); }}>
        {deleting ? <DialogContent title={locale === "zh" ? "删除自定义模型" : "Delete Custom Model"} description={deleting.id}>
          <div className="grid gap-2 px-5 py-5"><p className="text-copy-14 text-fg">{locale === "zh" ? `确认删除“${deleting.display_name}”？` : `Delete “${deleting.display_name}”?`}</p><p className="text-copy-13 text-fg-muted">{locale === "zh" ? "删除后客户端将不能再使用此模型 ID。系统预设只能停用，不会出现在此操作中。" : "Clients will no longer be able to use this model ID. Catalog presets can only be disabled and never appear here."}</p></div>
          <FormActions><DialogClose asChild><Button type="button" variant="secondary" disabled={deletePending}>{t("common.cancel")}</Button></DialogClose><Button type="button" variant="danger" loading={deletePending} onClick={() => void removeModel()}><Trash2 className="size-3.5" />{t("common.delete")}</Button></FormActions>
        </DialogContent> : null}
      </Dialog>
    </ContentFrame>
  );
}

function ModelEditor({ model, onSaved }: { model?: ModelSpec; onSaved: () => Promise<void> }) {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const editing = Boolean(model);
  const [enabled, setEnabled] = useState(model?.enabled ?? true);
  const [preferBest, setPreferBest] = useState(Boolean(model?.prefer_best));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const tierOptions = [
    { value: "", label: locale === "zh" ? "不限" : "No minimum" },
    { value: "basic", label: "Basic" },
    { value: "super", label: "Super" },
    { value: "pro", label: "Pro" },
    { value: "premium", label: "Premium" },
    { value: "heavy", label: "Heavy" },
    { value: "enterprise", label: "Enterprise" },
  ];
  if (model?.minimum_tier && !tierOptions.some((option) => option.value === model.minimum_tier)) {
    tierOptions.push({ value: model.minimum_tier, label: model.minimum_tier });
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSaving(true);
    setError("");
    const data = new FormData(event.currentTarget);
    const list = (name: string) => String(data.get(name) ?? "").split(",").map((value) => value.trim()).filter(Boolean);
    try {
      const id = String(data.get("id") ?? model?.id ?? "").trim();
      await apiFetch(editing ? `/models/${model?.id}` : "/models", {
        method: editing ? "PATCH" : "POST",
        ...jsonBody({
          ...(!editing ? { id } : {}),
          display_name: data.get("display_name"),
          upstream_model: data.get("upstream_model"),
          capability: data.get("capability"),
          credential_kinds: data.getAll("credential_kinds"),
          minimum_tier: data.get("minimum_tier"),
          aliases: list("aliases"),
          prefer_best: preferBest,
          enabled,
        }),
      });
      toast(locale === "zh" ? (editing ? "模型配置已保存" : "自定义模型已创建") : (editing ? "Model configuration saved" : "Custom model created"));
      await onSaved();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setSaving(false);
    }
  }

  return (
    <DialogContent title={locale === "zh" ? (editing ? "编辑模型" : "添加模型") : (editing ? "Edit Model" : "Add Model")} description={model?.id ?? (locale === "zh" ? "创建 Grok 上游映射" : "Create a Grok upstream mapping")} className="max-w-xl">
      <form onSubmit={submit}>
        <div className="grid gap-4 px-5 py-5 sm:grid-cols-2">
          {!editing ? <Input name="id" label={locale === "zh" ? "公开模型 ID" : "Public Model ID"} placeholder="grok-custom" pattern="[A-Za-z0-9._:-]+" description={locale === "zh" ? "创建后不可修改。" : "Cannot be changed after creation."} required /> : null}
          <Input name="display_name" label={locale === "zh" ? "公开名称" : "Public Name"} defaultValue={model?.display_name ?? ""} required />
          <Input name="upstream_model" label={locale === "zh" ? "上游映射" : "Upstream Mapping"} defaultValue={model?.upstream_model ?? ""} required />
          <Select name="capability" label={locale === "zh" ? "能力" : "Capability"} defaultValue={model?.capability ?? "chat"} options={(["chat", "responses", "messages", "image", "image_edit", "video"] as ModelSpec["capability"][]).map((value) => ({ value, label: capabilityLabel(value, locale) }))} />
          <Select name="minimum_tier" label={locale === "zh" ? "最低等级" : "Minimum Tier"} defaultValue={model?.minimum_tier ?? ""} options={tierOptions} />
          <Input name="aliases" label={locale === "zh" ? "别名" : "Aliases"} defaultValue={model?.aliases?.join(", ") ?? ""} description={locale === "zh" ? "使用逗号分隔多个别名" : "Separate aliases with commas"} className="font-mono" />
          <div className="grid gap-1.5">
            <span className="text-label-13 font-medium text-fg">{locale === "zh" ? "凭据类型" : "Credential Kinds"}</span>
            <div className="flex min-h-9 flex-wrap items-center gap-x-4 gap-y-2 rounded-[6px] border border-border bg-subtle px-3 py-2">
              {credentialKinds.map((kind) => <label key={kind} className="flex items-center gap-2 text-label-13 text-fg-muted"><input name="credential_kinds" value={kind} type="checkbox" defaultChecked={model ? model.credential_kinds.includes(kind) : kind === "grok_sso"} className="size-4 accent-[var(--ds-blue-700)]" />{kind}</label>)}
            </div>
          </div>
          <div className="border-y border-border py-3 sm:col-span-2"><Switch checked={preferBest} onCheckedChange={setPreferBest} label={locale === "zh" ? "优先最高等级账号" : "Prefer Highest Account Tier"} description={locale === "zh" ? "按 Heavy、Super、Basic 的顺序选择首个可用账号池。" : "Select the first available pool in Heavy, Super, Basic order."} /></div>
          <div className="sm:col-span-2"><Switch checked={enabled} onCheckedChange={setEnabled} label={locale === "zh" ? "对客户端公开此模型" : "Expose this model to clients"} description={locale === "zh" ? "停用后，模型不会出现在公开目录中。" : "Disabled models are removed from the public catalog."} /></div>
          {error ? <p role="alert" className="text-copy-13 text-danger sm:col-span-2">{error}</p> : null}
        </div>
        <FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button type="submit" loading={saving}>{editing ? t("common.save") : (locale === "zh" ? "创建模型" : "Create Model")}</Button></FormActions>
      </form>
    </DialogContent>
  );
}
