import { expect, test, type Page } from "@playwright/test";

async function authenticate(page: Page) {
  await page.route("**/admin/api/auth/me", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { id: "admin-1", email: "admin@example.com" } }) }));
}

test("account import accepts multiple OAuth files and exposes refresh metadata", async ({ page }) => {
  await authenticate(page);
  const now = new Date().toISOString();
  const account = {
    id: "oauth-account",
    name: "OAuth Primary",
    email: "oauth@example.com",
    kind: "cli_oauth",
    tier: "basic",
    status: "active",
    priority: 100,
    concurrency_limit: 4,
    health_score: 1,
    failure_count: 0,
    credential_expires_at: "2030-01-02T03:04:05Z",
    quota: {},
    created_at: now,
    updated_at: now,
  };
  let importBody = "";
  let refreshed = false;
  await page.route("**/admin/api/accounts**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/accounts/import") && route.request().method() === "POST") {
      importBody = route.request().postData() ?? "";
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { imported: 2, failed: 0, items: [] } }) });
      return;
    }
    if (path.endsWith("/accounts/quota-summary")) {
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { total_accounts: 1, available_accounts: 1, requests: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 }, tokens: { state: "unknown", limit: null, used: null, remaining: null, usage_percent: null, known_accounts: 0, unknown_accounts: 1, unlimited_accounts: 0, window_count: 0 } } }) });
      return;
    }
    if (path.endsWith("/accounts/policy")) {
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { strategy: "affinity" } }) });
      return;
    }
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { items: [account], total: 1 } }) });
  });
  await page.route("**/admin/api/proxies**", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { items: [], total: 0 } }) }));
  await page.route("**/admin/api/oauth/refresh/oauth-account", async (route) => {
    refreshed = true;
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: account }) });
  });

  await page.goto("/accounts/");
  await expect(page.locator('time[datetime="2030-01-02T03:04:05Z"]')).toBeVisible();
  await page.getByRole("button", { name: /批量导入|Import/ }).click();
  const fileInput = page.locator('input[type="file"][multiple]');
  await fileInput.setInputFiles([
    { name: "first.json", mimeType: "application/json", buffer: Buffer.from('{"type":"xai"}') },
    { name: "second.json", mimeType: "application/json", buffer: Buffer.from('{"type":"xai"}') },
  ]);
  await expect(page.getByText(/已选择 2 个文件|2 files selected/)).toBeVisible();
  await expect(page.getByText("first.json", { exact: true })).toBeVisible();
  await expect(page.getByText("second.json", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: /开始导入|Import/, exact: true }).click();
  await expect.poll(() => importBody.includes("first.json") && importBody.includes("second.json")).toBe(true);

  await page.getByRole("button", { name: /刷新 OAuth 凭据|Refresh OAuth credentials/ }).click();
  await expect.poll(() => refreshed).toBe(true);
});

test("model editor submits complete routing configuration", async ({ page, isMobile }) => {
  test.skip(Boolean(isMobile), "table editing is covered in the desktop project");
  await authenticate(page);
  const model = { id: "grok-4.5", upstream_model: "grok-4.5", display_name: "Grok 4.5", capability: "chat", credential_kinds: ["cli_oauth"], minimum_tier: "", aliases: ["grok-4.5-cli", "grok-latest"], prefer_best: false, catalog_managed: true, enabled: true, created_at: new Date().toISOString(), updated_at: new Date().toISOString() };
  let submitted: Record<string, unknown> | undefined;
  await page.route("**/admin/api/models**", async (route) => {
    if (route.request().method() === "PATCH") {
      submitted = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { ...model, ...submitted } }) });
      return;
    }
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { items: [model], total: 1 } }) });
  });

  await page.goto("/models/");
  await page.getByRole("button", { name: /编辑模型|Edit model/ }).click();
  await page.getByLabel(/公开名称|Public Name/).fill("Grok Production");
  await page.getByRole("button", { name: /保存更改|Save Changes/ }).click();
  await expect.poll(() => submitted?.display_name).toBe("Grok Production");
  expect(submitted?.credential_kinds).toEqual(["cli_oauth"]);
});

test("proxy and media destructive actions require confirmation", async ({ page, isMobile }) => {
  test.skip(Boolean(isMobile), "table actions are covered in the desktop project");
  await authenticate(page);
  const now = new Date().toISOString();
  let proxyDeleted = false;
  await page.route("**/admin/api/proxies**", async (route) => {
    if (route.request().method() === "DELETE") {
      proxyDeleted = true;
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { deleted: true } }) });
      return;
    }
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { items: [{ id: "proxy-1", name: "Tokyo", enabled: true, healthy: true, last_checked_at: now, created_at: now, updated_at: now }], total: 1 } }) });
  });
  await page.goto("/proxies/");
  await page.getByRole("button", { name: /删除代理|Delete proxy/ }).click();
  expect(proxyDeleted).toBe(false);
  await page.getByRole("dialog").getByRole("button", { name: /删除|Delete/ }).click();
  await expect.poll(() => proxyDeleted).toBe(true);

  let mediaDeleted = false;
  await page.route("**/admin/api/media**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "DELETE") {
      mediaDeleted = true;
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { deleted: true } }) });
      return;
    }
    if (path.endsWith("/media/summary")) {
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { total_objects: 1, total_bytes: 1024, image_objects: 1, image_bytes: 1024, video_objects: 0, video_bytes: 0, expiring_soon_objects: 1, expiring_soon_bytes: 1024, expiring_before: new Date(Date.now() + 86400000).toISOString() } }) });
      return;
    }
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { items: [{ id: "media-1", kind: "image", content_type: "image/png", size: 1024, created_at: now, expires_at: new Date(Date.now() + 3600000).toISOString() }], total: 1 } }) });
  });
  await page.goto("/media/");
  await page.getByRole("button", { name: /删除媒体文件|Delete media/ }).click();
  expect(mediaDeleted).toBe(false);
  await page.getByRole("dialog").getByRole("button", { name: /删除|Delete/ }).click();
  await expect.poll(() => mediaDeleted).toBe(true);
});

test("log cleanup sends a retention timestamp", async ({ page, isMobile }) => {
  test.skip(Boolean(isMobile), "cleanup behavior is covered in the desktop project");
  await authenticate(page);
  let before = "";
  await page.route("**/admin/api/logs**", async (route) => {
    if (route.request().method() === "DELETE") {
      before = new URL(route.request().url()).searchParams.get("before") ?? "";
      await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { deleted: 4 } }) });
      return;
    }
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ data: { items: [], total: 0 } }) });
  });
  await page.goto("/logs/");
  await page.getByRole("button", { name: /清理日志|Clean Up/ }).click();
  await page.getByRole("dialog").getByRole("button", { name: /执行清理|Clean Up/ }).click();
  await expect.poll(() => before).not.toBe("");
  expect(Number.isNaN(Date.parse(before))).toBe(false);
});
