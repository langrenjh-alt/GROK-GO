export const API_BASE = "/admin/api";
const CSRF_COOKIE = "grok_go_csrf";
const CSRF_HEADER = "X-CSRF-Token";

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status: number,
    public readonly code?: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

type ApiEnvelope<T> = { data?: T; error?: { code?: string; message?: string }; message?: string };

async function apiRequest(path: string, init?: RequestInit): Promise<Response> {
  const method = (init?.method ?? "GET").toUpperCase();
  const headers = new Headers(init?.headers);
  if (!headers.has("Accept")) headers.set("Accept", "application/json");
  const isFormData = typeof FormData !== "undefined" && init?.body instanceof FormData;
  if (init?.body && !isFormData && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (method !== "GET" && method !== "HEAD" && !headers.has(CSRF_HEADER)) {
    const csrfToken = readCookie(CSRF_COOKIE);
    if (csrfToken) headers.set(CSRF_HEADER, csrfToken);
  }
  return fetch(`${API_BASE}${path}`, {
    ...init,
    cache: init?.cache ?? "no-store",
    credentials: "same-origin",
    headers,
  });
}

async function throwIfApiError(response: Response): Promise<void> {
  if (response.ok) return;
  const contentType = response.headers.get("content-type") ?? "";
  let envelope: ApiEnvelope<unknown> | undefined;
  if (contentType.includes("application/json")) {
    try {
      envelope = (await response.json()) as ApiEnvelope<unknown>;
    } catch {
      // Fall through to the status-based message for malformed error bodies.
    }
  }
  throw new ApiError(
    envelope?.error?.message ?? envelope?.message ?? `Request failed (${response.status})`,
    response.status,
    envelope?.error?.code,
  );
}

export async function apiFetchResponse(path: string, init?: RequestInit): Promise<Response> {
  const response = await apiRequest(path, init);
  await throwIfApiError(response);
  return response;
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await apiFetchResponse(path, init);
  const contentType = response.headers.get("content-type") ?? "";
  const payload = contentType.includes("application/json")
    ? ((await response.json()) as ApiEnvelope<T> | T)
    : undefined;
  if (payload && typeof payload === "object" && "data" in payload) {
    return (payload as ApiEnvelope<T>).data as T;
  }
  return payload as T;
}

export function readCookie(name: string): string | undefined {
  if (typeof document === "undefined") return undefined;
  const prefix = `${encodeURIComponent(name)}=`;
  for (const part of document.cookie.split(";")) {
    const cookie = part.trim();
    if (cookie.startsWith(prefix)) return decodeURIComponent(cookie.slice(prefix.length));
  }
  return undefined;
}

export function jsonBody(value: unknown): Pick<RequestInit, "body"> {
  return { body: JSON.stringify(value) };
}
