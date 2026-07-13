"use client";

import { useCallback, useEffect, useState } from "react";
import { apiFetch } from "./api";

export function useResource<T>(path: string, initial: T) {
  const [data, setData] = useState<T>(initial);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setData(await apiFetch<T>(path));
    } catch (reason) {
      setError(reason instanceof Error ? reason : new Error("Unknown error"));
    } finally {
      setLoading(false);
    }
  }, [path]);
  useEffect(() => {
    // Start the client-side request when the resource path changes.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);
  return { data, setData, loading, error, reload: load };
}
