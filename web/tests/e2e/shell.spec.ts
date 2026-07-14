import { expect, test } from "@playwright/test";

test("desktop console shell exposes primary navigation", async ({ page, isMobile }) => {
  test.skip(Boolean(isMobile), "desktop project only");
  await page.route("**/admin/api/auth/me", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { id: "admin-1", email: "admin@example.com" } }) }));
  await page.route("**/admin/api/dashboard", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { requests_24h: 0, success_rate: 100, avg_latency_ms: 0, tokens_24h: 0, input_tokens_24h: 0, cached_tokens_24h: 0, usage_samples_24h: 0, cache_hit_rate: 0, active_accounts: 0, total_accounts: 0, active_keys: 0, gateway_healthy: true, hourly_requests: [], hourly_input_tokens: [], hourly_cached_tokens: [], hourly_usage_samples: [], hourly_cache_hit_rate: [], recent_logs: [] } }) }));
  await page.goto("/dashboard/");
  await expect(page.getByRole("link", { name: /GROK-GO/ })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Primary" })).toBeVisible();
  await expect(page.getByRole("heading", { name: /运行概览|Runtime Overview/ })).toBeVisible();
});

test("account filters, import, OAuth, and debugger controls are available", async ({ page, isMobile }) => {
  test.skip(Boolean(isMobile), "desktop project only");
  await page.route("**/admin/api/auth/me", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { id: "admin-1", email: "admin@example.com" } }) }));
  await page.route("**/admin/api/accounts", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { items: [], total: 0 } }) }));
  await page.goto("/accounts/");
  await expect(page.getByLabel(/按凭据类型筛选|Filter by credential type/)).toBeVisible();
  await expect(page.getByLabel(/按账号等级筛选|Filter by account tier/)).toBeVisible();
  await expect(page.getByLabel(/按绑定代理筛选|Filter by bound proxy/)).toBeVisible();
  await expect(page.getByLabel(/账号调度策略|Account scheduling strategy/)).toBeVisible();
  await page.getByRole("button", { name: /批量导入|Import/ }).click();
  await expect(page.getByRole("dialog", { name: /批量导入账号|Import Accounts/ })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "OAuth" }).click();
  await expect(page.getByRole("dialog", { name: /添加 CLI OAuth 账号|Add CLI OAuth Account/ })).toBeVisible();
  await page.keyboard.press("Escape");

  await page.goto("/debugger/");
  await expect(page.getByRole("tab", { name: "Multipart" })).toBeVisible();
  await expect(page.getByRole("switch", { name: /流式响应|Stream Response/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /发送请求|Send Request/ })).toBeVisible();
  expect(await page.getByRole("button", { name: /发送请求|Send Request/ }).evaluate((element) => getComputedStyle(element).color !== getComputedStyle(element).backgroundColor)).toBe(true);
  await expect(page.getByRole("button", { name: /cURL/i })).toBeVisible();
  await page.getByLabel("Endpoint").selectOption("/v1/models");
  await expect(page.getByLabel("Method")).toHaveValue("GET");
  await expect(page.getByRole("switch", { name: /流式响应|Stream Response/ })).toBeDisabled();
});

test("mobile navigation opens as a dialog", async ({ page, isMobile }) => {
  test.skip(!isMobile, "mobile project only");
  await page.route("**/admin/api/auth/me", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { id: "admin-1", email: "admin@example.com" } }) }));
  const account = {
    id: "account-1",
    name: "Mobile account",
    email: "mobile@example.com",
    kind: "cli_oauth",
    tier: "basic",
    status: "active",
    priority: 100,
    concurrency_limit: 4,
    health_score: 1,
    failure_count: 0,
    quota: {},
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  };
  await page.route("**/admin/api/accounts**", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/accounts/quota-summary")) {
      return route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { total_accounts: 1, available_accounts: 1, requests: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 }, tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 } } }) });
    }
    if (path.endsWith("/accounts/policy")) {
      return route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { strategy: "affinity" } }) });
    }
    return route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { items: [account], total: 1 } }) });
  });
  await page.route("**/admin/api/proxies**", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { items: [], total: 0 } }) }));
  await page.goto("/accounts/");
  const table = page.locator("table");
  await expect(table).toBeVisible();
  const tableScroller = table.locator("..");
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth === document.documentElement.clientWidth)).toBe(true);
  expect(await tableScroller.evaluate((element) => element.scrollWidth)).toBeGreaterThan(await tableScroller.evaluate((element) => element.clientWidth));
  await page.getByRole("button", { name: /打开导航|Open menu/ }).click();
  await expect(page.getByRole("dialog", { name: "Navigation" })).toBeVisible();
});

test("login redirects to setup before bootstrap", async ({ page }) => {
  await page.route("**/admin/api/status", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { bootstrapped: false } }) }));
  await page.goto("/login/");
  await expect(page).toHaveURL(/\/setup\/$/);
  await expect(page.getByRole("heading", { name: /初始化 GROK-GO|Set Up GROK-GO/ })).toBeVisible();
});
