import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@/components/i18n-provider";
import { ToastProvider } from "@/components/ui/toast";
import { AccountsView } from "./accounts-view";

const account = {
  id: "account-1",
  name: "Primary",
  kind: "grok_sso",
  tier: "super",
  status: "active",
  priority: 100,
  concurrency_limit: 4,
  health_score: 1,
  failure_count: 0,
  quota: { requests_limit: 100, requests_remaining: 60 },
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const originalCreateObjectURL = Object.getOwnPropertyDescriptor(URL, "createObjectURL");
const originalRevokeObjectURL = Object.getOwnPropertyDescriptor(URL, "revokeObjectURL");

describe("AccountsView", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    if (originalCreateObjectURL) Object.defineProperty(URL, "createObjectURL", originalCreateObjectURL);
    else Reflect.deleteProperty(URL, "createObjectURL");
    if (originalRevokeObjectURL) Object.defineProperty(URL, "revokeObjectURL", originalRevokeObjectURL);
    else Reflect.deleteProperty(URL, "revokeObjectURL");
  });

  it("updates scheduling strategy and batch edits selected accounts", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      let data: unknown = {};
      if (path.includes("/accounts?")) data = { items: [account], total: 1 };
      if (path.endsWith("/accounts/quota-summary")) data = {
        total_accounts: 1,
        available_accounts: 1,
        requests: { state: "known", limit: 100, used: 40, remaining: 60, usage_percent: 40, known_accounts: 1, unknown_accounts: 0, unlimited_accounts: 0, window_count: 1 },
        tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 },
      };
      if (path.endsWith("/proxies")) data = { items: [{ id: "proxy-1", name: "Primary proxy", enabled: true, healthy: true, created_at: account.created_at, updated_at: account.updated_at }], total: 1 };
      if (path.endsWith("/accounts/policy")) data = { strategy: init?.method === "PUT" ? "round_robin" : "affinity" };
      if (path.endsWith("/accounts/batch")) data = { updated: 1, items: [{ ...account, status: "disabled" }] };
      return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<I18nProvider><ToastProvider><AccountsView /></ToastProvider></I18nProvider>);
    expect(await screen.findByText("Primary")).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("账号调度策略"), "round_robin");
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/admin/api/accounts/policy", expect.objectContaining({ method: "PUT" })));

    await user.click(screen.getByLabelText("选择 Primary"));
    await user.click(screen.getByRole("button", { name: "批量编辑 (1)" }));
    await user.click(screen.getByLabelText("修改启用状态"));
    await user.selectOptions(screen.getByLabelText("批量状态"), "disabled");
    await user.click(screen.getByLabelText("修改账号等级"));
    await user.selectOptions(screen.getByLabelText("批量账号等级"), "super");
    await user.click(screen.getByLabelText("修改并发限制"));
    await user.clear(screen.getByLabelText("批量并发限制"));
    await user.type(screen.getByLabelText("批量并发限制"), "8");
    await user.click(screen.getByLabelText("修改绑定代理"));
    await user.selectOptions(screen.getByLabelText("批量绑定代理"), "proxy-1");
    await user.click(screen.getByLabelText("修改标签"));
    await user.type(screen.getByLabelText("批量标签"), "production, primary");
    await user.click(screen.getByRole("button", { name: "应用更改" }));

    await waitFor(() => {
      const request = fetchMock.mock.calls.find(([path]) => String(path).endsWith("/accounts/batch"));
      expect(request?.[1]).toEqual(expect.objectContaining({ method: "PATCH" }));
      expect(JSON.parse(String(request?.[1]?.body))).toEqual({ ids: ["account-1"], status: "disabled", tier: "super", concurrency_limit: 8, proxy_id: "proxy-1", tags: ["production", "primary"] });
    });
  });

  it("toggles an account and saves its concurrency limit", async () => {
    let current = { ...account };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      let data: unknown = {};
      if (path.includes("/accounts?")) data = { items: [current], total: 1 };
      if (path.endsWith("/accounts/quota-summary")) data = {
        total_accounts: 1,
        available_accounts: current.status === "active" ? 1 : 0,
        requests: { state: "known", limit: 100, used: 40, remaining: 60, usage_percent: 40, known_accounts: 1, unknown_accounts: 0, unlimited_accounts: 0, window_count: 1 },
        tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 },
      };
      if (path.endsWith("/accounts/policy")) data = { strategy: "affinity" };
      if (path.endsWith("/proxies")) data = { items: [], total: 0 };
      if (path.endsWith("/accounts/account-1") && init?.method === "PATCH") {
        current = { ...current, ...JSON.parse(String(init.body)) };
        data = current;
      }
      return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<I18nProvider><ToastProvider><AccountsView /></ToastProvider></I18nProvider>);
    expect(await screen.findByText("Primary")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "停用账号" }));
    await waitFor(() => {
      const request = fetchMock.mock.calls.find(([path, init]) => String(path).endsWith("/accounts/account-1") && init?.method === "PATCH");
      expect(JSON.parse(String(request?.[1]?.body))).toEqual({ status: "disabled" });
    });
    await screen.findByRole("button", { name: "启用账号" });

    await user.click(screen.getByRole("button", { name: "编辑账号" }));
    await user.selectOptions(screen.getByLabelText("状态"), "active");
    const concurrencyInput = screen.getByRole("spinbutton", { name: /并发限制/ });
    await user.clear(concurrencyInput);
    await user.type(concurrencyInput, "8");
    await user.click(screen.getByRole("button", { name: /保存/ }));

    await waitFor(() => {
      const requests = fetchMock.mock.calls.filter(([path, init]) => String(path).endsWith("/accounts/account-1") && init?.method === "PATCH");
      expect(JSON.parse(String(requests.at(-1)?.[1]?.body))).toEqual({ name: "Primary", tier: "super", status: "active", priority: 100, concurrency_limit: 8 });
    });
  });

  it("runs single and bounded batch account probes", async () => {
    const failedAccount = { ...account, status: "disabled", health_score: 0.25, failure_count: 1, last_error: "upstream returned HTTP 403" };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      let data: unknown = {};
      if (path.includes("/accounts?")) data = { items: [account], total: 1 };
      if (path.endsWith("/accounts/quota-summary")) data = {
        total_accounts: 1,
        available_accounts: 1,
        requests: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 },
        tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 },
      };
      if (path.endsWith("/accounts/policy")) data = { strategy: "affinity" };
      if (path.endsWith("/proxies")) data = { items: [], total: 0 };
      if (path.endsWith("/accounts/account-1/probe") && init?.method === "POST") data = {
        account_id: account.id,
        success: false,
        status_code: 403,
        duration_ms: 42,
        model: "grok-4.20-fast",
        message: "upstream returned HTTP 403: account blocked",
        completed_at: "2026-07-14T00:00:00Z",
        account: failedAccount,
      };
      if (path.endsWith("/accounts/probe") && init?.method === "POST") data = {
        total: 1,
        succeeded: 1,
        failed: 0,
        items: [{ account_id: account.id, success: true, status_code: 200, duration_ms: 31, model: "grok-4.20-fast", message: "ok", completed_at: "2026-07-14T00:00:00Z", account }],
      };
      return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<I18nProvider><ToastProvider><AccountsView /></ToastProvider></I18nProvider>);
    expect(await screen.findByText("Primary")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "探测账号连接" }));
    expect(await screen.findByRole("dialog", { name: "账号探测结果" })).toBeVisible();
    expect(screen.getByText("upstream returned HTTP 403: account blocked")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "完成" }));

    await user.click(screen.getByLabelText("选择 Primary"));
    await user.click(screen.getByRole("button", { name: "批量探测 (1)" }));
    expect(await screen.findByText("上游返回了可解析的有效响应。")).toBeVisible();
    await waitFor(() => {
      const request = fetchMock.mock.calls.find(([path, init]) => String(path).endsWith("/accounts/probe") && init?.method === "POST");
      expect(JSON.parse(String(request?.[1]?.body))).toEqual({ ids: ["account-1"] });
    });
  });

  it("imports multiple files and exposes OAuth expiry and manual refresh", async () => {
    const oauthAccount = {
      ...account,
      id: "account-oauth",
      name: "OAuth Primary",
      kind: "cli_oauth",
      credential_expires_at: "2030-01-02T03:04:05Z",
    };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      let data: unknown = {};
      if (path.includes("/accounts?")) data = { items: [oauthAccount], total: 1 };
      if (path.endsWith("/accounts/quota-summary")) data = {
        total_accounts: 1,
        available_accounts: 1,
        requests: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 },
        tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 },
      };
      if (path.endsWith("/accounts/policy")) data = { strategy: "affinity" };
      if (path.endsWith("/proxies")) data = { items: [], total: 0 };
      if (path.endsWith("/accounts/import") && init?.method === "POST") data = { imported: 2, failed: 0, items: [] };
      if (path.endsWith("/oauth/refresh/account-oauth") && init?.method === "POST") data = oauthAccount;
      return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<I18nProvider><ToastProvider><AccountsView /></ToastProvider></I18nProvider>);
    expect(await screen.findByText("OAuth Primary")).toBeInTheDocument();
    expect(document.querySelector('time[datetime="2030-01-02T03:04:05Z"]')).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "批量导入" }));
    const fileInput = screen.getByLabelText("选择文件") as HTMLInputElement;
    expect(fileInput).toHaveAttribute("multiple");
    expect(fileInput).toHaveClass("sr-only");
    const files = [
      new File(['{"type":"xai"}'], "first.json", { type: "application/json" }),
      new File(['{"type":"xai"}'], "second.json", { type: "application/json" }),
    ];
    await user.upload(fileInput, files);
    expect(screen.getByText("已选择 2 个文件")).toBeVisible();
    expect(screen.getByText("first.json")).toBeVisible();
    expect(screen.getByText("second.json")).toBeVisible();

    await user.click(screen.getByRole("button", { name: "开始导入" }));
    await waitFor(() => {
      const request = fetchMock.mock.calls.find(([path, init]) => String(path).endsWith("/accounts/import") && init?.method === "POST");
      const body = request?.[1]?.body as FormData | undefined;
      expect(body?.getAll("files").map((file) => (file as File).name)).toEqual(["first.json", "second.json"]);
    });

    await user.click(screen.getByRole("button", { name: "刷新 OAuth 凭据" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/admin/api/oauth/refresh/account-oauth", expect.objectContaining({ method: "POST" })));
  });

  it("prompts for an explicit account selection before export", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      void init;
      const path = String(input);
      let data: unknown = {};
      if (path.includes("/accounts?")) data = { items: [account], total: 1 };
      if (path.endsWith("/accounts/quota-summary")) data = {
        total_accounts: 1,
        available_accounts: 1,
        requests: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 },
        tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 },
      };
      if (path.endsWith("/accounts/policy")) data = { strategy: "affinity" };
      if (path.endsWith("/proxies")) data = { items: [], total: 0 };
      if (path.endsWith("/auth/me")) data = { id: "admin-1", email: "admin@example.com", totp_enabled: false };
      return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<I18nProvider><ToastProvider><AccountsView /></ToastProvider></I18nProvider>);
    expect(await screen.findByText("Primary")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "导出账号" }));

    expect(await screen.findByText("请先选择要导出的账号。")).toBeVisible();
    expect(fetchMock.mock.calls.some(([path, init]) => String(path).endsWith("/accounts/export") && init?.method === "POST")).toBe(false);
  });

  it("exports only selected IDs with TOTP and downloads the named attachment", async () => {
    const secondAccount = { ...account, id: "account-2", name: "Secondary", kind: "cli_oauth" };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      let data: unknown = {};
      if (path.includes("/accounts?")) data = { items: [account, secondAccount], total: 2 };
      if (path.endsWith("/accounts/quota-summary")) data = {
        total_accounts: 2,
        available_accounts: 2,
        requests: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 2, unlimited_accounts: 0, window_count: 0 },
        tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 2, unlimited_accounts: 0, window_count: 0 },
      };
      if (path.endsWith("/accounts/policy")) data = { strategy: "affinity" };
      if (path.endsWith("/proxies")) data = { items: [], total: 0 };
      if (path.endsWith("/auth/me")) data = { id: "admin-1", email: "admin@example.com", totp_enabled: true };
      if (path.endsWith("/accounts/export") && init?.method === "POST") {
        return new Response('{"accounts":[{"id":"account-1"}]}', {
          status: 200,
          headers: {
            "Content-Type": "application/json",
            "Content-Disposition": "attachment; filename*=UTF-8''grok%20accounts.json",
          },
        });
      }
      return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const createObjectURL = vi.fn(() => "blob:grok-accounts");
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revokeObjectURL });
    let downloadedFilename = "";
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      downloadedFilename = this.download;
    });
    const user = userEvent.setup();

    render(<I18nProvider><ToastProvider><AccountsView /></ToastProvider></I18nProvider>);
    expect(await screen.findByText("Primary")).toBeInTheDocument();
    expect(screen.getByText("Secondary")).toBeInTheDocument();
    await user.click(screen.getByLabelText("选择 Primary"));
    await user.click(screen.getByRole("button", { name: "导出账号" }));

    expect(await screen.findByRole("dialog", { name: "导出账号" })).toBeVisible();
    await user.selectOptions(screen.getByLabelText("导出格式"), "grok2api");
    await user.type(screen.getByLabelText(/当前密码/), "admin-secret");
    await user.type(await screen.findByLabelText(/双重验证码/), "123456");
    await user.click(screen.getByRole("button", { name: "确认并导出" }));

    await waitFor(() => {
      const request = fetchMock.mock.calls.find(([path, init]) => String(path).endsWith("/accounts/export") && init?.method === "POST");
      expect(JSON.parse(String(request?.[1]?.body))).toEqual({
        format: "grok2api",
        ids: ["account-1"],
        current_password: "admin-secret",
        totp_code: "123456",
      });
    });
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:grok-accounts"));
    expect(createObjectURL).toHaveBeenCalledWith(expect.any(Blob));
    expect(clickSpy).toHaveBeenCalledOnce();
    expect(downloadedFilename).toBe("grok accounts.json");
  });

  it("shows export compatibility errors in the dialog", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      let data: unknown = {};
      if (path.includes("/accounts?")) data = { items: [account], total: 1 };
      if (path.endsWith("/accounts/quota-summary")) data = {
        total_accounts: 1,
        available_accounts: 1,
        requests: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 },
        tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 },
      };
      if (path.endsWith("/accounts/policy")) data = { strategy: "affinity" };
      if (path.endsWith("/proxies")) data = { items: [], total: 0 };
      if (path.endsWith("/auth/me")) data = { id: "admin-1", email: "admin@example.com", totp_enabled: false };
      if (path.endsWith("/accounts/export") && init?.method === "POST") {
        return new Response(JSON.stringify({ error: { code: "incompatible_accounts", message: "sub2api export only supports CLI OAuth accounts" } }), { status: 400, headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<I18nProvider><ToastProvider><AccountsView /></ToastProvider></I18nProvider>);
    expect(await screen.findByText("Primary")).toBeInTheDocument();
    await user.click(screen.getByLabelText("选择 Primary"));
    await user.click(screen.getByRole("button", { name: "导出账号" }));
    await user.selectOptions(screen.getByLabelText("导出格式"), "sub2api");
    await user.type(screen.getByLabelText(/当前密码/), "admin-secret");
    await user.click(screen.getByRole("button", { name: "确认并导出" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("sub2api export only supports CLI OAuth accounts");
    expect(screen.getByRole("dialog", { name: "导出账号" })).toBeVisible();
  });
});
