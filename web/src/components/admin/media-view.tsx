"use client";

import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Clock3, FileImage, Film, RefreshCw, Trash2 } from "lucide-react";
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
import { formatBytes, formatRelative } from "@/lib/format";
import type { ListResponse, MediaObject } from "@/lib/types";
import { normalizeList } from "@/lib/types";
import { useResource } from "@/lib/use-resource";
import { DEFAULT_PAGE_SIZE, paginatedPath } from "@/lib/pagination";
import { usePageClamp } from "@/lib/use-page-clamp";
import { ContentFrame, FormActions, PageFooter, Toolbar } from "./shared";

interface MediaSummary {
  total_objects: number;
  total_bytes: number;
  image_objects: number;
  image_bytes: number;
  video_objects: number;
  video_bytes: number;
  expiring_soon_objects: number;
  expiring_soon_bytes: number;
  expiring_before: string;
}

interface MediaDeleteResult {
  requested: number;
  deleted: number;
  deleted_bytes: number;
  failed: number;
  errors?: Array<{ id: string; message: string }>;
}

type MaintenanceMode = "batch" | "expired" | "all";

const emptySummary: MediaSummary = {
  total_objects: 0,
  total_bytes: 0,
  image_objects: 0,
  image_bytes: 0,
  video_objects: 0,
  video_bytes: 0,
  expiring_soon_objects: 0,
  expiring_soon_bytes: 0,
  expiring_before: "",
};

export function MediaView() {
  const { t, locale } = useI18n();
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE);
  const resource = useResource<MediaObject[] | ListResponse<MediaObject>>(paginatedPath("/media", page, pageSize), []);
  const summaryResource = useResource<MediaSummary>("/media/summary", emptySummary);
  const [deleteTarget, setDeleteTarget] = useState<MediaObject | null>(null);
  const [maintenance, setMaintenance] = useState<MaintenanceMode | null>(null);
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(() => new Set());
  const mediaList = normalizeList(resource.data);
  const allRows = mediaList.items;
  const total = mediaList.total ?? allRows.length;
  usePageClamp(page, pageSize, total, setPage);
  const rows = useMemo(
    () => allRows.filter((item) => (filter === "all" || item.kind === filter) && `${item.id} ${item.kind} ${item.content_type}`.toLowerCase().includes(search.toLowerCase())),
    [allRows, filter, search],
  );
  const allVisibleSelected = rows.length > 0 && rows.every((item) => selectedIDs.has(item.id));

  async function reload() {
    await Promise.all([resource.reload(), summaryResource.reload()]);
  }

  function selectOne(id: string, checked: boolean) {
    setSelectedIDs((current) => {
      const next = new Set(current);
      if (checked) next.add(id); else next.delete(id);
      return next;
    });
  }

  function selectVisible(checked: boolean) {
    setSelectedIDs((current) => {
      const next = new Set(current);
      for (const item of rows) {
        if (checked) next.add(item.id); else next.delete(item.id);
      }
      return next;
    });
  }

  const columns: ColumnDef<MediaObject>[] = [
    { id: "select", enableSorting: false, header: () => <input type="checkbox" aria-label={locale === "zh" ? "选择当前媒体" : "Select visible media"} checked={allVisibleSelected} onChange={(event) => selectVisible(event.target.checked)} className="size-4 accent-blue-700" />, cell: ({ row }) => <input type="checkbox" aria-label={locale === "zh" ? `选择 ${row.original.id}` : `Select ${row.original.id}`} checked={selectedIDs.has(row.original.id)} onChange={(event) => selectOne(row.original.id, event.target.checked)} className="size-4 accent-blue-700" /> },
    { accessorKey: "id", header: locale === "zh" ? "文件" : "Object", cell: ({ row }) => <div className="flex items-center gap-2.5">{row.original.kind === "video" ? <Film className="size-4 shrink-0 text-fg-muted" /> : <FileImage className="size-4 shrink-0 text-fg-muted" />}<code className="block max-w-[220px] truncate text-label-13" title={row.original.id}>{row.original.id}</code></div> },
    { accessorKey: "kind", header: locale === "zh" ? "类型" : "Kind", cell: ({ row }) => <Badge tone={row.original.kind === "video" ? "amber" : "blue"}>{row.original.kind}</Badge> },
    { accessorKey: "content_type", header: locale === "zh" ? "内容类型" : "Content Type", cell: ({ row }) => <code className="text-label-13">{row.original.content_type}</code> },
    { accessorKey: "size", header: locale === "zh" ? "大小" : "Size", cell: ({ row }) => <span className="font-mono text-label-13 tabular-nums">{formatBytes(row.original.size)}</span> },
    { accessorKey: "created_at", header: locale === "zh" ? "创建时间" : "Created", cell: ({ row }) => <span className="whitespace-nowrap text-copy-13 text-fg-muted" title={new Date(row.original.created_at).toLocaleString()}>{formatRelative(row.original.created_at, locale)}</span> },
    { accessorKey: "expires_at", header: locale === "zh" ? "过期时间" : "Expires", cell: ({ row }) => <span className="whitespace-nowrap text-copy-13 text-fg-muted" title={new Date(row.original.expires_at).toLocaleString()}>{formatRelative(row.original.expires_at, locale)}</span> },
    { id: "actions", enableSorting: false, header: () => <span className="sr-only">{t("common.actions")}</span>, cell: ({ row }) => <IconButton label={locale === "zh" ? "删除媒体文件" : "Delete media"} variant="tertiary" onClick={() => setDeleteTarget(row.original)}><Trash2 className="size-4 text-danger" /></IconButton> },
  ];

  return (
    <ContentFrame>
      <PageHeader title={t("media.title")} description={t("media.description")} actions={<><Button size="small" variant="secondary" onClick={() => void reload()} loading={resource.loading || summaryResource.loading}><RefreshCw className="size-3.5" />{t("common.refresh")}</Button><Button size="small" variant="secondary" onClick={() => setMaintenance("expired")}><Clock3 className="size-3.5" />{locale === "zh" ? "清理过期" : "Clean Expired"}</Button><Button size="small" variant="secondary" className="text-danger" disabled={summaryResource.data.total_objects === 0} onClick={() => setMaintenance("all")}><Trash2 className="size-3.5" />{locale === "zh" ? "清空缓存" : "Clear Cache"}</Button></>} />
      <MediaSummaryBand summary={summaryResource.data} loading={summaryResource.loading || Boolean(summaryResource.error)} locale={locale} />
      <Toolbar
        search={search}
        onSearch={(value) => { setSearch(value); setPage(1); }}
        placeholder={locale === "zh" ? "搜索文件 ID、类型或 MIME" : "Search object ID, kind, or MIME"}
        filter={filter}
        onFilter={(value) => { setFilter(value); setPage(1); }}
        filterOptions={[{ value: "all", label: locale === "zh" ? "全部媒体" : "All media" }, { value: "image", label: locale === "zh" ? "图片" : "Image" }, { value: "video", label: locale === "zh" ? "视频" : "Video" }]}
        trailing={<><span className="text-copy-13 tabular-nums text-fg-subtle">{rows.length} / {allRows.length}</span><Button className="w-full sm:w-auto" size="small" variant="secondary" disabled={selectedIDs.size === 0} onClick={() => setMaintenance("batch")}><Trash2 className="size-3.5" />{locale === "zh" ? `删除所选 (${selectedIDs.size})` : `Delete selected (${selectedIDs.size})`}</Button></>}
      />
      {resource.loading && !allRows.length ? <LoadingState label={t("common.loading")} /> : resource.error ? <ErrorState title={t("common.requestFailed")} description={resource.error.message} onRetry={() => void reload()} /> : rows.length ? <DataTable ariaLabel={t("media.title")} data={rows} columns={columns} getRowId={(row) => row.id} /> : <EmptyState title={locale === "zh" ? "没有媒体文件" : "No media objects"} description={locale === "zh" ? "图片或视频生成结果将在这里出现。" : "Generated image and video results will appear here."} />}
      <PageFooter count={allRows.length} noun={locale === "zh" ? "个文件" : "objects"} total={total} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={(value) => { setPageSize(value); setPage(1); }} locale={locale} />

      <Dialog open={Boolean(deleteTarget)} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}>
        {deleteTarget ? <DeleteMediaDialog item={deleteTarget} onDeleted={async () => { setDeleteTarget(null); setSelectedIDs((current) => { const next = new Set(current); next.delete(deleteTarget.id); return next; }); await reload(); }} /> : null}
      </Dialog>

      <Dialog open={Boolean(maintenance)} onOpenChange={(open) => { if (!open) setMaintenance(null); }}>
        {maintenance ? <MaintenanceDialog mode={maintenance} ids={[...selectedIDs]} summary={summaryResource.data} onCompleted={async () => { setMaintenance(null); setSelectedIDs(new Set()); await reload(); }} /> : null}
      </Dialog>
    </ContentFrame>
  );
}

function MediaSummaryBand({ summary, loading, locale }: { summary: MediaSummary; loading: boolean; locale: "zh" | "en" }) {
  const value = (content: string | number) => loading ? "-" : typeof content === "number" ? content.toLocaleString() : content;
  return (
    <section aria-label={locale === "zh" ? "媒体缓存汇总" : "Media cache summary"} className="grid grid-cols-2 border-t border-border bg-surface md:grid-cols-5">
      <SummaryMetric label={locale === "zh" ? "缓存对象" : "Objects"} value={value(summary.total_objects)} className="border-r" />
      <SummaryMetric label={locale === "zh" ? "占用空间" : "Total Size"} value={value(formatBytes(summary.total_bytes))} className="md:border-r" />
      <SummaryMetric label={locale === "zh" ? "图片" : "Images"} value={value(summary.image_objects)} detail={loading ? undefined : formatBytes(summary.image_bytes)} className="border-r" />
      <SummaryMetric label={locale === "zh" ? "视频" : "Videos"} value={value(summary.video_objects)} detail={loading ? undefined : formatBytes(summary.video_bytes)} className="md:border-r" />
      <SummaryMetric label={locale === "zh" ? "24 小时内过期" : "Expiring in 24h"} value={value(summary.expiring_soon_objects)} detail={loading ? undefined : formatBytes(summary.expiring_soon_bytes)} className="col-span-2 border-b-0 md:col-span-1 md:border-r-0" />
    </section>
  );
}

function SummaryMetric({ label, value, detail, className = "" }: { label: string; value: string | number; detail?: string; className?: string }) {
  return <div className={`min-w-0 border-b border-border px-4 py-3 sm:px-6 lg:px-8 ${className}`}><div className="truncate text-label-13 text-fg-muted">{label}</div><div className="mt-1 truncate text-heading-20 font-semibold tabular-nums text-fg">{value}</div><div className="mt-0.5 min-h-4 truncate text-[11px] tabular-nums text-fg-subtle">{detail ?? " "}</div></div>;
}

function MaintenanceDialog({ mode, ids, summary, onCompleted }: { mode: MaintenanceMode; ids: string[]; summary: MediaSummary; onCompleted: () => Promise<void> }) {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [confirmation, setConfirmation] = useState("");
  const [working, setWorking] = useState(false);
  const [error, setError] = useState("");
  const title = mode === "batch" ? (locale === "zh" ? "删除所选媒体" : "Delete Selected Media") : mode === "expired" ? (locale === "zh" ? "清理过期媒体" : "Clean Expired Media") : (locale === "zh" ? "清空媒体缓存" : "Clear Media Cache");

  async function execute() {
    setWorking(true);
    setError("");
    try {
      const result = mode === "batch"
        ? await apiFetch<MediaDeleteResult>("/media/batch-delete", { method: "POST", ...jsonBody({ ids }) })
        : await apiFetch<MediaDeleteResult>("/media/cleanup", { method: "POST", ...jsonBody({ mode, ...(mode === "all" ? { confirm: true } : {}) }) });
      const message = locale === "zh" ? `已删除 ${result.deleted} 个对象，释放 ${formatBytes(result.deleted_bytes)}` : `Deleted ${result.deleted} objects and freed ${formatBytes(result.deleted_bytes)}`;
      toast(result.failed ? `${message}; ${result.failed} failed` : message, result.failed ? "error" : "success");
      await onCompleted();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setWorking(false);
    }
  }

  const blocked = mode === "all" && confirmation !== "CLEAR";
  return (
    <DialogContent title={title} description={mode === "batch" ? (locale === "zh" ? `${ids.length} 个已选对象` : `${ids.length} selected objects`) : mode === "expired" ? (locale === "zh" ? "删除所有已经超过到期时间的缓存对象。" : "Delete every cache object past its expiration time.") : (locale === "zh" ? `${summary.total_objects} 个对象，${formatBytes(summary.total_bytes)}` : `${summary.total_objects} objects, ${formatBytes(summary.total_bytes)}`)}>
      <div className="grid gap-4 px-5 py-5">
        <p className="text-copy-14 text-fg-muted">{mode === "batch" ? (locale === "zh" ? "所选文件及其签名下载地址将立即失效。" : "Selected files and their signed download links will expire immediately.") : mode === "expired" ? (locale === "zh" ? "仍在有效期内的图片和视频会被保留。" : "Images and videos that have not expired are preserved.") : (locale === "zh" ? "全部 Grok 图片和视频缓存都将被删除，此操作不可撤销。" : "Every cached Grok image and video will be deleted. This action is permanent.")}</p>
        {mode === "all" ? <Input label={locale === "zh" ? "输入 CLEAR 以确认" : "Type CLEAR to confirm"} value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="off" placeholder="CLEAR" /> : null}
        {error ? <p role="alert" className="text-copy-13 text-danger">{error}</p> : null}
      </div>
      <FormActions><DialogClose asChild><Button type="button" variant="secondary" disabled={working}>{t("common.cancel")}</Button></DialogClose><Button type="button" variant={mode === "expired" ? "primary" : "danger"} loading={working} disabled={blocked} onClick={() => void execute()}>{mode === "expired" ? <Clock3 className="size-4" /> : <Trash2 className="size-4" />}{mode === "expired" ? (locale === "zh" ? "开始清理" : "Clean Expired") : t("common.delete")}</Button></FormActions>
    </DialogContent>
  );
}

function DeleteMediaDialog({ item, onDeleted }: { item: MediaObject; onDeleted: () => Promise<void> }) {
  const { t, locale } = useI18n();
  const { toast } = useToast();
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState("");

  async function remove() {
    setDeleting(true);
    setError("");
    try {
      await apiFetch(`/media/${item.id}`, { method: "DELETE" });
      toast(locale === "zh" ? "媒体文件已删除" : "Media deleted");
      await onDeleted();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setDeleting(false);
    }
  }

  return (
    <DialogContent title={locale === "zh" ? "删除媒体文件" : "Delete Media Object"} description={item.id}>
      <div className="grid gap-3 px-5 py-5"><p className="text-copy-14 text-fg-muted">{locale === "zh" ? "缓存文件及其签名下载地址将立即失效，此操作不可撤销。" : "The cached file and its signed download links will expire immediately. This action is permanent."}</p><dl className="grid grid-cols-2 divide-x divide-border border-y border-border text-copy-13"><div className="py-3 pr-3"><dt className="text-fg-subtle">{locale === "zh" ? "类型" : "Type"}</dt><dd className="mt-0.5 break-all font-mono text-fg">{item.content_type}</dd></div><div className="py-3 pl-3"><dt className="text-fg-subtle">{locale === "zh" ? "大小" : "Size"}</dt><dd className="mt-0.5 font-mono text-fg">{formatBytes(item.size)}</dd></div></dl>{error ? <p role="alert" className="text-copy-13 text-danger">{error}</p> : null}</div>
      <FormActions><DialogClose asChild><Button type="button" variant="secondary">{t("common.cancel")}</Button></DialogClose><Button variant="danger" loading={deleting} onClick={() => void remove()}><Trash2 className="size-4" />{t("common.delete")}</Button></FormActions>
    </DialogContent>
  );
}
