import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@/components/i18n-provider";
import { ToastProvider } from "@/components/ui/toast";
import { KeysView } from "./keys-view";

const resource = vi.hoisted(() => ({
  data: [{
    id: "key-1",
    name: "Production",
    prefix: "grok_abc123",
    secret_available: true,
    enabled: true,
    rpm: 0,
    concurrency_limit: 0,
    daily_request_limit: 0,
    monthly_token_limit: 0,
    created_at: "2026-07-13T00:00:00Z",
    updated_at: "2026-07-13T00:00:00Z",
  }],
  loading: false,
  error: null,
  reload: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/lib/use-resource", () => ({ useResource: () => resource }));

function response(data: unknown) {
  return Promise.resolve(new Response(JSON.stringify({ data }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  }));
}

function renderKeys() {
  return render(<I18nProvider><ToastProvider><KeysView /></ToastProvider></I18nProvider>);
}

describe("KeysView management actions", () => {
  beforeEach(() => {
    resource.reload.mockClear();
    window.localStorage.clear();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/keys/key-1/reveal") && init?.method === "POST") return response({ key: "grok_full_secret" });
      if (url.endsWith("/keys/key-1") && init?.method === "DELETE") return response({ deleted: true });
      if (url.endsWith("/keys/key-1") && init?.method === "PATCH") return response({ ...resource.data[0], ...JSON.parse(String(init.body)) });
      throw new Error(`Unexpected request: ${init?.method ?? "GET"} ${url}`);
    }));
  });

  afterEach(() => vi.unstubAllGlobals());

  it("reveals the encrypted key on demand and copies it", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText");
    renderKeys();

    await user.click(screen.getByRole("button", { name: "复制密钥" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith("grok_full_secret"));
    expect(vi.mocked(fetch)).toHaveBeenCalledWith("/admin/api/keys/key-1/reveal", expect.objectContaining({ method: "POST" }));
  });

  it("toggles a key directly from the table", async () => {
    const user = userEvent.setup();
    renderKeys();

    await user.click(screen.getByRole("button", { name: "停用密钥" }));

    await waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalledWith("/admin/api/keys/key-1", expect.objectContaining({ method: "PATCH" })));
    const request = vi.mocked(fetch).mock.calls.find(([path, init]) => String(path).endsWith("/keys/key-1") && init?.method === "PATCH");
    expect(JSON.parse(String(request?.[1]?.body))).toEqual({ enabled: false });
  });

  it("edits key limits from the actions menu", async () => {
    const user = userEvent.setup();
    renderKeys();

    await user.click(screen.getByRole("button", { name: "操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "编辑" }));
    const dialog = await screen.findByRole("dialog", { name: "编辑访问密钥" });
    await user.clear(within(dialog).getByLabelText(/名称/));
    await user.type(within(dialog).getByLabelText(/名称/), "Production v2");
    await user.click(within(dialog).getByRole("button", { name: "保存更改" }));

    await waitFor(() => {
      const request = vi.mocked(fetch).mock.calls.find(([path, init]) => String(path).endsWith("/keys/key-1") && init?.method === "PATCH");
      expect(JSON.parse(String(request?.[1]?.body))).toEqual(expect.objectContaining({ name: "Production v2", rpm: 0, concurrency_limit: 0 }));
    });
  });

  it("deletes a key after confirmation and reloads the list", async () => {
    const user = userEvent.setup();
    renderKeys();

    await user.click(screen.getByRole("button", { name: "操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "删除" }));
    await user.click(await screen.findByRole("button", { name: "删除" }));

    await waitFor(() => expect(resource.reload).toHaveBeenCalled());
    expect(vi.mocked(fetch)).toHaveBeenCalledWith("/admin/api/keys/key-1", expect.objectContaining({ method: "DELETE" }));
  });
});
