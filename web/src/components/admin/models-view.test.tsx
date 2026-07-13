import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@/components/i18n-provider";
import { ToastProvider } from "@/components/ui/toast";
import { ModelsView } from "./models-view";

const resource = vi.hoisted(() => ({
  data: [
    {
      id: "grok-imagine-image-edit",
      upstream_model: "auto",
      display_name: "Grok Imagine Image Edit",
      capability: "image_edit",
      credential_kinds: ["grok_sso"],
      minimum_tier: "super",
      aliases: ["grok-imagine-edit"],
      prefer_best: false,
      catalog_managed: true,
      enabled: true,
      created_at: "2026-07-14T00:00:00Z",
      updated_at: "2026-07-14T00:00:00Z",
    },
    {
      id: "grok-4.3-high",
      upstream_model: "grok-4.3",
      display_name: "Grok 4.3 High Thinking",
      capability: "chat",
      credential_kinds: ["console_sso"],
      minimum_tier: "",
      aliases: [],
      prefer_best: false,
      catalog_managed: false,
      enabled: true,
      created_at: "2026-07-14T00:00:00Z",
      updated_at: "2026-07-14T00:00:00Z",
    },
  ],
  loading: false,
  error: null,
  reload: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/lib/use-resource", () => ({
  useResource: (path: string) => ({
    ...resource,
    data: path.includes("capability=image_edit") ? [resource.data[0]] : resource.data,
  }),
}));

function renderModels() {
  return render(<I18nProvider><ToastProvider><ModelsView /></ToastProvider></I18nProvider>);
}

describe("ModelsView Grok catalog", () => {
  beforeEach(() => {
    resource.reload.mockClear();
    window.localStorage.clear();
    vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => new Response(JSON.stringify({ data: { ...resource.data[0], ...JSON.parse(String(init?.body ?? "{}")), catalog_managed: false } }), { status: 200, headers: { "Content-Type": "application/json" } })));
  });

  afterAll(() => vi.unstubAllGlobals());

  it("shows catalog source, precise media capability, and upstream route", async () => {
    const user = userEvent.setup();
    renderModels();

    expect(screen.getByText("系统预设")).toBeVisible();
    const imageRow = screen.getByText("Grok Imagine Image Edit").closest("tr");
    expect(imageRow).not.toBeNull();
    expect(within(imageRow as HTMLElement).getByText("图片编辑")).toBeVisible();
    expect(screen.getByText("grok.com SSO")).toBeVisible();
    expect(screen.getByText("Console SSO")).toBeVisible();

    await user.selectOptions(screen.getByLabelText("Filter"), "image_edit");
    expect(screen.getByText("Grok Imagine Image Edit")).toBeVisible();
    expect(screen.queryByText("Grok 4.3 High Thinking")).not.toBeInTheDocument();
  });

  it("edits image-edit and highest-tier routing metadata", async () => {
    const user = userEvent.setup();
    renderModels();
    const row = screen.getByText("Grok Imagine Image Edit").closest("tr");
    expect(row).not.toBeNull();
    await user.click(within(row as HTMLElement).getByRole("button", { name: "编辑模型" }));

    const dialog = await screen.findByRole("dialog", { name: "编辑模型" });
    await user.click(within(dialog).getByRole("switch", { name: /优先最高等级账号/ }));
    await user.click(within(dialog).getByRole("button", { name: "保存更改" }));

    await waitFor(() => {
      const request = vi.mocked(fetch).mock.calls.find(([path, init]) => String(path).endsWith("/models/grok-imagine-image-edit") && init?.method === "PATCH");
      expect(request).toBeDefined();
      expect(JSON.parse(String(request?.[1]?.body))).toEqual(expect.objectContaining({
        capability: "image_edit",
        credential_kinds: ["grok_sso"],
        minimum_tier: "super",
        prefer_best: true,
      }));
    });
  });

  it("creates a custom Grok model", async () => {
    const user = userEvent.setup();
    renderModels();
    await user.click(screen.getByRole("button", { name: "添加模型" }));
    const dialog = await screen.findByRole("dialog", { name: "添加模型" });
    await user.type(within(dialog).getByLabelText(/公开模型 ID/), "grok-custom-fast");
    await user.type(within(dialog).getByLabelText(/公开名称/), "Grok Custom Fast");
    await user.type(within(dialog).getByLabelText(/上游映射/), "fast");
    await user.click(within(dialog).getByRole("button", { name: "创建模型" }));

    await waitFor(() => {
      const request = vi.mocked(fetch).mock.calls.find(([path, init]) => String(path).endsWith("/models") && init?.method === "POST");
      expect(request).toBeDefined();
      expect(JSON.parse(String(request?.[1]?.body))).toEqual(expect.objectContaining({
        id: "grok-custom-fast",
        display_name: "Grok Custom Fast",
        upstream_model: "fast",
        capability: "chat",
        credential_kinds: ["grok_sso"],
      }));
    });
  });

  it("deletes only a custom model after confirmation", async () => {
    const user = userEvent.setup();
    renderModels();
    const catalogRow = screen.getByText("Grok Imagine Image Edit").closest("tr");
    const customRow = screen.getByText("Grok 4.3 High Thinking").closest("tr");
    expect(within(catalogRow as HTMLElement).queryByRole("button", { name: "删除模型" })).not.toBeInTheDocument();
    await user.click(within(customRow as HTMLElement).getByRole("button", { name: "删除模型" }));
    const dialog = await screen.findByRole("dialog", { name: "删除自定义模型" });
    await user.click(within(dialog).getByRole("button", { name: "删除" }));

    await waitFor(() => {
      const request = vi.mocked(fetch).mock.calls.find(([path, init]) => String(path).endsWith("/models/grok-4.3-high") && init?.method === "DELETE");
      expect(request).toBeDefined();
    });
  });
});
