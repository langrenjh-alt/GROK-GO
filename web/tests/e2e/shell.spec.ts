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

test("account import, OAuth, and debugger controls are available", async ({ page, isMobile }) => {
  test.skip(Boolean(isMobile), "desktop project only");
  await page.route("**/admin/api/auth/me", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { id: "admin-1", email: "admin@example.com" } }) }));
  await page.route("**/admin/api/accounts", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { items: [], total: 0 } }) }));
  await page.goto("/accounts/");
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
  await page.route("**/admin/api/accounts", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: [] }) }));
  await page.goto("/accounts/");
  await page.getByRole("button", { name: /打开导航|Open menu/ }).click();
  await expect(page.getByRole("dialog", { name: "Navigation" })).toBeVisible();
});

test("login redirects to setup before bootstrap", async ({ page }) => {
  await page.route("**/admin/api/status", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { bootstrapped: false } }) }));
  await page.goto("/login/");
  await expect(page).toHaveURL(/\/setup\/$/);
  await expect(page.getByRole("heading", { name: /初始化 GROK-GO|Set Up GROK-GO/ })).toBeVisible();
});
