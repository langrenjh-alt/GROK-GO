"use client";

import { useMemo, useState } from "react";
import { flexRender, getCoreRowModel, getSortedRowModel, useReactTable, type ColumnDef, type SortingState } from "@tanstack/react-table";
import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";
import { cn } from "@/lib/cn";

export function DataTable<T>({ data, columns, getRowId, ariaLabel, className }: { data: T[]; columns: ColumnDef<T>[]; getRowId?: (row: T) => string; ariaLabel: string; className?: string }) {
  const [sorting, setSorting] = useState<SortingState>([]);
  const stableColumns = useMemo(() => columns, [columns]);
  // TanStack Table intentionally exposes stateful closures that React Compiler skips.
  // eslint-disable-next-line react-hooks/incompatible-library
  const table = useReactTable({
    data,
    columns: stableColumns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getRowId,
  });
  return (
    <div className={cn("scrollbar w-full min-w-0 max-w-[100dvw] overflow-x-auto border-y border-border bg-surface", className)}>
      <table aria-label={ariaLabel} className="w-full min-w-[760px] border-collapse text-left text-sm">
        <thead className="bg-subtle text-fg-muted">
          {table.getHeaderGroups().map((group) => (
            <tr key={group.id}>
              {group.headers.map((header) => {
                const sorted = header.column.getIsSorted();
                return (
                  <th key={header.id} scope="col" aria-sort={sorted === "asc" ? "ascending" : sorted === "desc" ? "descending" : "none"} className="h-10 whitespace-nowrap border-b border-border px-4 text-label-13 font-medium">
                    {header.isPlaceholder ? null : header.column.getCanSort() ? (
                      <button type="button" onClick={header.column.getToggleSortingHandler()} className="inline-flex h-8 items-center gap-1.5 rounded-[5px] outline-none hover:text-fg focus-visible:shadow-focus">
                        {flexRender(header.column.columnDef.header, header.getContext())}
                        {sorted === "asc" ? <ArrowUp className="size-3.5" /> : sorted === "desc" ? <ArrowDown className="size-3.5" /> : <ChevronsUpDown className="size-3.5 opacity-50" />}
                      </button>
                    ) : flexRender(header.column.columnDef.header, header.getContext())}
                  </th>
                );
              })}
            </tr>
          ))}
        </thead>
        <tbody>
          {table.getRowModel().rows.map((row) => (
            <tr key={row.id} className="border-b border-border last:border-b-0 hover:bg-subtle/60">
              {row.getVisibleCells().map((cell) => <td key={cell.id} className="h-12 max-w-[320px] px-4 text-fg">{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
