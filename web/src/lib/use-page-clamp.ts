"use client";

import { useEffect } from "react";

export function usePageClamp(page: number, pageSize: number, total: number, onPageChange: (page: number) => void) {
  useEffect(() => {
    const lastPage = Math.max(1, Math.ceil(total / pageSize));
    if (page > lastPage) onPageChange(lastPage);
  }, [onPageChange, page, pageSize, total]);
}
