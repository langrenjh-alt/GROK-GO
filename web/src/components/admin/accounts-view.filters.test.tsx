import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@/components/i18n-provider";
import { ToastProvider } from "@/components/ui/toast";
import { AccountsView } from "./accounts-view";

const account = {
  id: "account-1",
  name: "Primary",
  kind: "cli_oauth",
  tier: "super",
  status: "active",
  priority: 100,
  concurrency_limit: 4,
  health_score: 1,
  failure_count: 0,
  quota: {},
  created_at: "2026-07-14T00:00:00Z",
  updated_at: "2026-07-14T00:00:00Z",
};

function dataResponse(data: unknown) {
  return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
}

describe("AccountsView filters", () => {
  afterEach(() => vi.restoreAllMocks());

  it("combines search, status, credential, tier, proxy, and pagination parameters", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/accounts?")) return dataResponse({ items: [account], total: 60 });
      if (path.includes("/proxies")) return dataResponse({ items: [{ id: "proxy-1", name: "Primary proxy", enabled: true, healthy: true, created_at: account.created_at, updated_at: account.updated_at }], total: 1 });
      if (path.endsWith("/accounts/quota-summary")) return dataResponse({ total_accounts: 1, available_accounts: 1, requests: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 }, tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 } });
      if (path.endsWith("/accounts/policy")) return dataResponse({ strategy: "affinity" });
      if (path.endsWith("/auth/me")) return dataResponse({ id: "admin-1", email: "admin@example.test", totp_enabled: false });
      return dataResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<I18nProvider><ToastProvider><AccountsView /></ToastProvider></I18nProvider>);
    expect(await screen.findByText("Primary")).toBeInTheDocument();
    await screen.findByRole("option", { name: "Primary proxy" });
    expect(fetchMock.mock.calls.some(([input]) => String(input) === "/admin/api/proxies?page=1&page_size=500")).toBe(true);

    await user.selectOptions(screen.getByLabelText("按账号状态筛选"), "active");
    await user.selectOptions(screen.getByLabelText("按凭据类型筛选"), "cli_oauth");
    await user.selectOptions(screen.getByLabelText("按账号等级筛选"), "super");
    await user.selectOptions(screen.getByLabelText("按绑定代理筛选"), "proxy-1");
    await user.type(screen.getByLabelText("搜索名称、邮箱或标签"), "Primary");

    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input) === "/admin/api/accounts?page=1&page_size=25&q=Primary&status=active&kind=cli_oauth&tier=super&proxy_id=proxy-1")).toBe(true));
    await user.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input) === "/admin/api/accounts?page=2&page_size=25&q=Primary&status=active&kind=cli_oauth&tier=super&proxy_id=proxy-1")).toBe(true));
  });

  it("supports direct-only filtering and resets every account filter", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/accounts?")) return dataResponse({ items: [account], total: 1 });
      if (path.includes("/proxies")) return dataResponse({ items: [], total: 0 });
      if (path.endsWith("/accounts/quota-summary")) return dataResponse({ total_accounts: 1, available_accounts: 1, requests: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 }, tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 } });
      if (path.endsWith("/accounts/policy")) return dataResponse({ strategy: "affinity" });
      if (path.endsWith("/auth/me")) return dataResponse({ id: "admin-1", email: "admin@example.test", totp_enabled: false });
      return dataResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<I18nProvider><ToastProvider><AccountsView /></ToastProvider></I18nProvider>);
    expect(await screen.findByText("Primary")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("按绑定代理筛选"), "direct");
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input) === "/admin/api/accounts?page=1&page_size=25&proxy_id=direct")).toBe(true));

    await user.click(screen.getByRole("button", { name: "重置筛选" }));
    expect(screen.getByLabelText("按绑定代理筛选")).toHaveValue("all");
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input]) => String(input) === "/admin/api/accounts?page=1&page_size=25").length).toBeGreaterThan(1));
  });
});
