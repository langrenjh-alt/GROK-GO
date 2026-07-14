"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { apiFetch } from "./api";

export function useResource<T>(path: string, initial: T) {
  const [data, setData] = useState<T>(initial);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const requestGeneration = useRef(0);
  const activeResult = useRef<{
    controller: AbortController;
    resolve: (value: boolean | PromiseLike<boolean>) => void;
  } | null>(null);
  const load = useCallback(() => {
    const generation = ++requestGeneration.current;
    const controller = new AbortController();
    let resolveResult!: (value: boolean | PromiseLike<boolean>) => void;
    const result = new Promise<boolean>((resolve) => {
      resolveResult = resolve;
    });
    const previousResult = activeResult.current;
    activeResult.current = { controller, resolve: resolveResult };
    previousResult?.controller.abort();
    previousResult?.resolve(result);

    setLoading(true);
    setError(null);
    void (async () => {
      try {
        const next = await apiFetch<T>(path, { signal: controller.signal });
        if (generation !== requestGeneration.current) return;
        setData(next);
        resolveResult(true);
      } catch (reason) {
        if (generation !== requestGeneration.current) return;
        setError(reason instanceof Error ? reason : new Error("Unknown error"));
        resolveResult(false);
      } finally {
        if (generation === requestGeneration.current) setLoading(false);
      }
    })();
    return result;
  }, [path]);
  useEffect(() => {
    // Start the client-side request when the resource path changes.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);
  useEffect(() => () => {
    requestGeneration.current++;
    activeResult.current?.controller.abort();
    activeResult.current?.resolve(false);
    activeResult.current = null;
  }, []);
  return { data, setData, loading, error, reload: load };
}
