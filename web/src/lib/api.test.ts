import { API_BASE, apiFetch, apiFetchResponse } from "./api";

describe("apiFetch", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    document.cookie = "grok_go_csrf=; Max-Age=0; Path=/";
  });

  it("uses the same-origin admin API and unwraps data", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: { ok: true } }), { status: 200, headers: { "content-type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(apiFetch<{ ok: boolean }>("/health")).resolves.toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledWith(`${API_BASE}/health`, expect.objectContaining({ cache: "no-store", credentials: "same-origin" }));
  });

  it("returns structured API failures", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: "invalid", message: "Invalid request" } }), { status: 400, headers: { "content-type": "application/json" } })));
    await expect(apiFetch("/test")).rejects.toMatchObject({ status: 400, code: "invalid", message: "Invalid request" });
  });

  it("adds the CSRF cookie to non-GET requests", async () => {
    document.cookie = "grok_go_csrf=csrf_token_123; Path=/";
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await apiFetch("/settings", { method: "PATCH", body: JSON.stringify({ enabled: true }) });

    const init = fetchMock.mock.calls[0][1] as RequestInit;
    const headers = new Headers(init.headers);
    expect(headers.get("X-CSRF-Token")).toBe("csrf_token_123");
    expect(headers.get("Content-Type")).toBe("application/json");
  });

  it("does not add CSRF to GET or force a multipart content type", async () => {
    document.cookie = "grok_go_csrf=csrf_token_123; Path=/";
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await apiFetch("/status");
    const getHeaders = new Headers((fetchMock.mock.calls[0][1] as RequestInit).headers);
    expect(getHeaders.has("X-CSRF-Token")).toBe(false);

    const form = new FormData();
    form.set("file", new Blob(["data"]), "test.txt");
    await apiFetch("/upload", { method: "POST", body: form });
    const formHeaders = new Headers((fetchMock.mock.calls[1][1] as RequestInit).headers);
    expect(formHeaders.get("X-CSRF-Token")).toBe("csrf_token_123");
    expect(formHeaders.has("Content-Type")).toBe(false);
  });

  it("returns an unconsumed attachment response with its headers", async () => {
    document.cookie = "grok_go_csrf=csrf_token_123; Path=/";
    const fetchMock = vi.fn().mockResolvedValue(new Response("export-data", {
      status: 200,
      headers: {
        "content-type": "application/octet-stream",
        "content-disposition": "attachment; filename=accounts.json",
      },
    }));
    vi.stubGlobal("fetch", fetchMock);

    const response = await apiFetchResponse("/accounts/export", {
      method: "POST",
      headers: { Accept: "application/octet-stream" },
      body: JSON.stringify({ ids: ["account-1"] }),
    });

    expect(response.headers.get("content-disposition")).toBe("attachment; filename=accounts.json");
    await expect(response.text()).resolves.toBe("export-data");
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    const headers = new Headers(init.headers);
    expect(headers.get("Accept")).toBe("application/octet-stream");
    expect(headers.get("X-CSRF-Token")).toBe("csrf_token_123");
  });

  it("preserves structured API errors for raw responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: "incompatible_accounts", message: "Unsupported account type" } }), { status: 400, headers: { "content-type": "application/json" } })));
    await expect(apiFetchResponse("/accounts/export", { method: "POST" })).rejects.toMatchObject({ status: 400, code: "incompatible_accounts", message: "Unsupported account type" });
  });
});
