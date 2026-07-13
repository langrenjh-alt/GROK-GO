import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@/components/i18n-provider";
import { ToastProvider } from "@/components/ui/toast";
import { LogsView } from "./logs-view";

const resources = vi.hoisted(() => {
  const now = new Date().toISOString();
  return {
    paths: [] as string[],
    requests: { data: { items: [
      { id: "request-1", request_id: "req-1", model: "grok-4", endpoint: "/v1/responses", status_code: 500, duration_ms: 21, input_tokens: 10, output_tokens: 2, cached_tokens: 4, error_code: "upstream_error", created_at: now },
    ], total: 1 }, loading: false, error: null, reload: vi.fn().mockResolvedValue(undefined) },
    audit: { data: { items: [
      { id: "audit-1", admin_id: "admin-1", action: "api_key.update", resource_type: "api_key", resource_id: "key-1", ip_address: "203.0.113.10", metadata: { method: "PATCH", route: "/keys/{id}", status: 200, success: true, duration_ms: 12 }, created_at: now },
      { id: "audit-2", admin_id: "admin-1", action: "settings.update", resource_type: "settings", resource_id: "", ip_address: "203.0.113.10", metadata: { method: "PATCH", route: "/settings", status: 403, success: false, duration_ms: 1 }, created_at: now },
    ], total: 2 }, loading: false, error: null, reload: vi.fn().mockResolvedValue(undefined) },
  };
});

vi.mock("@/lib/use-resource", () => ({ useResource: (path: string) => {
  resources.paths.push(path);
  const params = new URLSearchParams(path.split("?")[1] ?? "");
  if (path.startsWith("/audit-logs")) {
    const query = (params.get("q") ?? "").toLowerCase();
    const resourceType = params.get("resource_type");
    const success = params.get("success");
    const items = resources.audit.data.items.filter((item) => (
      (!resourceType || item.resource_type === resourceType)
      && (!success || String(item.metadata.success) === success)
      && (!query || `${item.action} ${item.resource_type} ${item.resource_id} ${item.admin_id} ${item.ip_address}`.toLowerCase().includes(query))
    ));
    return { ...resources.audit, data: { ...resources.audit.data, items, total: items.length } };
  }
  const query = (params.get("q") ?? "").toLowerCase();
  const model = params.get("model");
  const statusClass = params.get("status_class");
  const items = resources.requests.data.items.filter((item) => (
    (!model || item.model === model)
    && (!statusClass || String(item.status_code).startsWith(statusClass[0]))
    && (!query || `${item.request_id} ${item.model} ${item.endpoint} ${item.error_code}`.toLowerCase().includes(query))
  ));
  return { ...resources.requests, data: { ...resources.requests.data, items, total: items.length } };
} }));

function renderLogs() {
  return render(<I18nProvider><ToastProvider><LogsView /></ToastProvider></I18nProvider>);
}

describe("LogsView audit trail", () => {
  beforeEach(() => {
    resources.audit.reload.mockClear();
    resources.paths.length = 0;
    window.localStorage.clear();
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response(JSON.stringify({ data: { deleted: 3 } }), { status: 200, headers: { "Content-Type": "application/json" } }))));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("switches to audit entries and filters failures", async () => {
    const user = userEvent.setup();
    renderLogs();
    await user.click(screen.getByRole("tab", { name: "操作审计" }));
    expect(screen.getByText("api_key.update")).toBeVisible();
    expect(screen.getByText("settings.update")).toBeVisible();
    await user.selectOptions(screen.getByLabelText("按结果筛选"), "failed");
    expect(screen.queryByText("api_key.update")).not.toBeInTheDocument();
    expect(screen.getByText("settings.update")).toBeVisible();
  });

  it("sends request and audit filters through their paginated resource paths", async () => {
    const user = userEvent.setup();
    renderLogs();
    await user.type(screen.getByRole("searchbox"), "req-1");
    await user.selectOptions(screen.getByLabelText("Filter"), "5xx");
    await user.selectOptions(screen.getByLabelText("按模型筛选"), "grok-4");
    await waitFor(() => expect(resources.paths.some((path) => path.startsWith("/logs?") && path.includes("q=req-1") && path.includes("status_class=5xx") && path.includes("model=grok-4") && path.includes("created_from="))).toBe(true));

    await user.click(screen.getByRole("tab", { name: "操作审计" }));
    await user.type(screen.getByRole("searchbox"), "settings");
    await user.selectOptions(screen.getByLabelText("Filter"), "settings");
    await user.selectOptions(screen.getByLabelText("按结果筛选"), "failed");
    await user.selectOptions(screen.getByLabelText("按审计时间筛选"), "day");
    await waitFor(() => expect(resources.paths.some((path) => path.startsWith("/audit-logs?") && path.includes("q=settings") && path.includes("resource_type=settings") && path.includes("success=false") && path.includes("created_from="))).toBe(true));
  });

  it("shows only sanitized audit metadata in details", async () => {
    const user = userEvent.setup();
    renderLogs();
    await user.click(screen.getByRole("tab", { name: "操作审计" }));
    await user.click(screen.getAllByRole("button", { name: "查看审计详情" })[0]);
    const dialog = await screen.findByRole("dialog", { name: "审计详情" });
    expect(within(dialog).getByText("/keys/{id}")).toBeVisible();
    expect(within(dialog).getByText(/不保存请求正文或凭据/)).toBeVisible();
  });

  it("cleans audit entries by retention window", async () => {
    const user = userEvent.setup();
    renderLogs();
    await user.click(screen.getByRole("tab", { name: "操作审计" }));
    await user.click(screen.getByRole("button", { name: "清理日志" }));
    const dialog = await screen.findByRole("dialog", { name: "清理操作审计" });
    await user.click(within(dialog).getByRole("button", { name: "执行清理" }));
    await waitFor(() => {
      const request = vi.mocked(fetch).mock.calls.find(([path]) => String(path).includes("/audit-logs?before="));
      expect(request?.[1]).toEqual(expect.objectContaining({ method: "DELETE" }));
    });
    expect(resources.audit.reload).toHaveBeenCalled();
  });
});
