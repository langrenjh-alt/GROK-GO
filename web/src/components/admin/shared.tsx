"use client";

import { ChevronLeft, ChevronRight, Search, SlidersHorizontal } from "lucide-react";
import { Input, Select } from "@/components/ui/form";
import { IconButton } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import type { AccountStatus } from "@/lib/types";

export function ContentFrame({ children }: { children: React.ReactNode }) {
  return <div className="min-w-0 pb-10">{children}</div>;
}

export function Toolbar({ search, onSearch, placeholder, filter, onFilter, filterOptions, filters, trailing }: { search: string; onSearch: (value: string) => void; placeholder: string; filter?: string; onFilter?: (value: string) => void; filterOptions?: Array<{ label: string; value: string }>; filters?: React.ReactNode; trailing?: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-2 border-y border-border bg-surface px-4 py-3 sm:flex-row sm:flex-wrap sm:items-center sm:px-6 lg:px-8">
      <div className="w-full sm:max-w-[320px]"><Input type="search" aria-label={placeholder} placeholder={placeholder} value={search} onChange={(event) => onSearch(event.target.value)} prefix={<Search className="size-3.5" />} /></div>
      {filterOptions && onFilter ? <div className="w-full sm:w-44"><Select aria-label="Filter" value={filter} onChange={(event) => onFilter(event.target.value)} options={filterOptions} /></div> : null}
      {filters}
      <div className="flex w-full flex-wrap items-center gap-2 sm:ml-auto sm:w-auto">{trailing ?? <span className="hidden items-center gap-1.5 text-copy-13 text-fg-subtle sm:flex"><SlidersHorizontal className="size-3.5" />Filters</span>}</div>
    </div>
  );
}

export function AccountStatusBadge({ status }: { status: AccountStatus }) {
  const map: Record<AccountStatus, { tone: "green" | "amber" | "red" | "neutral"; label: string }> = {
    active: { tone: "green", label: "Active" },
    cooldown: { tone: "amber", label: "Cooldown" },
    expired: { tone: "red", label: "Expired" },
    disabled: { tone: "neutral", label: "Disabled" },
    error: { tone: "red", label: "Error" },
  };
  return <Badge tone={map[status].tone} dot>{map[status].label}</Badge>;
}

export function HealthBadge({ healthy, enabled = true }: { healthy: boolean; enabled?: boolean }) {
  if (!enabled) return <Badge tone="neutral" dot>Disabled</Badge>;
  return <Badge tone={healthy ? "green" : "red"} dot>{healthy ? "Healthy" : "Unhealthy"}</Badge>;
}

export function QuotaBar({ value, max }: { value: number; max: number }) {
  const percent = max > 0 ? Math.max(0, Math.min(100, (value / max) * 100)) : 0;
  return (
    <div className="grid min-w-32 gap-1">
      <div className="flex justify-between text-[11px] tabular-nums text-fg-muted"><span>{value.toLocaleString()}</span><span>{max.toLocaleString()}</span></div>
      <div className="h-1.5 overflow-hidden rounded-full bg-gray-300"><div className="h-full rounded-full bg-blue-700" style={{ width: `${percent}%` }} /></div>
    </div>
  );
}

interface PageFooterProps {
  count: number;
  noun: string;
  total?: number;
  page?: number;
  pageSize?: number;
  onPageChange?: (page: number) => void;
  onPageSizeChange?: (pageSize: number) => void;
  locale?: "zh" | "en";
}

export function PageFooter({ count, noun, total, page, pageSize, onPageChange, onPageSizeChange, locale = "en" }: PageFooterProps) {
  const paginated = total !== undefined && page !== undefined && pageSize !== undefined && onPageChange && onPageSizeChange;
  if (!paginated) {
    return <footer className="flex h-11 items-center border-b border-border bg-surface px-4 text-copy-13 tabular-nums text-fg-muted sm:px-6 lg:px-8">{count.toLocaleString()} {noun}</footer>;
  }
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const from = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = total === 0 ? 0 : Math.min(total, from + count - 1);
  return (
    <footer className="flex min-h-12 flex-col gap-2 border-b border-border bg-surface px-4 py-2 text-copy-13 tabular-nums text-fg-muted sm:flex-row sm:items-center sm:px-6 lg:px-8">
      <span>{from.toLocaleString()}-{to.toLocaleString()} / {total.toLocaleString()} {noun}</span>
      <div className="flex items-center gap-2 sm:ml-auto">
        <span className="hidden text-fg-subtle sm:inline">{locale === "zh" ? "每页" : "Rows"}</span>
        <Select
          aria-label={locale === "zh" ? "每页数量" : "Rows per page"}
          className="h-8 w-[72px]"
          value={String(pageSize)}
          onChange={(event) => onPageSizeChange(Number(event.target.value))}
          options={[10, 25, 50, 100].map((value) => ({ value: String(value), label: String(value) }))}
        />
        <span className="min-w-[72px] text-center text-fg-subtle">{page} / {totalPages}</span>
        <IconButton label={locale === "zh" ? "上一页" : "Previous page"} variant="tertiary" disabled={page <= 1} onClick={() => onPageChange(page - 1)}><ChevronLeft className="size-4" /></IconButton>
        <IconButton label={locale === "zh" ? "下一页" : "Next page"} variant="tertiary" disabled={page >= totalPages} onClick={() => onPageChange(page + 1)}><ChevronRight className="size-4" /></IconButton>
      </div>
    </footer>
  );
}

export function FormActions({ children }: { children: React.ReactNode }) {
  return <div className="sticky bottom-0 z-10 flex items-center justify-end gap-2 border-t border-border bg-surface px-5 py-4">{children}</div>;
}
