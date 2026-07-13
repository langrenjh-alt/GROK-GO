import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@/components/i18n-provider";
import { ToastProvider } from "@/components/ui/toast";
import { SettingsView } from "./settings-view";

const replace = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace }),
}));

const settings = {
  public_base_url: "https://api.example.test",
  request_timeout_seconds: 120,
  max_request_bytes: 33_554_432,
  max_concurrency: 32,
  log_retention_days: 30,
  cors_origins: "",
  trust_proxy_headers: false,
  defaults: {
    public_base_url: "https://api.example.test",
    request_timeout_seconds: 120,
    max_request_bytes: 33_554_432,
    max_concurrency: 32,
    log_retention_days: 30,
    cors_origins: "",
    trust_proxy_headers: false,
  },
  active: {
    public_base_url: "https://old.example.test",
    request_timeout_seconds: 120,
    max_request_bytes: 33_554_432,
    max_concurrency: 32,
    log_retention_days: 30,
    cors_origins: "",
    trust_proxy_headers: false,
  },
  restart_required: ["public_base_url"],
};

function jsonResponse(data: unknown) {
  return Promise.resolve(new Response(JSON.stringify({ data }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  }));
}

function renderSettings() {
  return render(<I18nProvider><ToastProvider><SettingsView /></ToastProvider></I18nProvider>);
}

describe("SettingsView administrator credentials", () => {
  let totpEnabled = false;

  beforeEach(() => {
    replace.mockReset();
    totpEnabled = false;
    window.localStorage.setItem("grok-go-locale", "en");
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/settings")) return jsonResponse(settings);
      if (url.endsWith("/auth/me")) return jsonResponse({ id: "admin-1", email: "admin@example.com", totp_enabled: totpEnabled });
      if (url.endsWith("/auth/totp/begin")) return jsonResponse({
        Secret: "JBSWY3DPEHPK3PXP",
        URI: "otpauth://totp/GROK-GO:admin%40example.com?issuer=GROK-GO&secret=JBSWY3DPEHPK3PXP",
        ExpiresAt: "2026-07-14T04:30:00Z",
      });
      if (url.endsWith("/auth/totp/confirm")) {
        totpEnabled = true;
        return jsonResponse({ totp_enabled: true });
      }
      if (url.endsWith("/auth/totp/disable")) {
        totpEnabled = false;
        return jsonResponse({ totp_enabled: false, reauthenticate: true });
      }
      if (url.endsWith("/auth/email")) return jsonResponse({ email_changed: true, reauthenticate: true });
      if (url.endsWith("/auth/password")) return jsonResponse({ password_changed: true, reauthenticate: true });
      throw new Error(`Unexpected request: ${url}`);
    }));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  it("normalizes the email request and redirects for reauthentication", async () => {
    const user = userEvent.setup();
    renderSettings();
    const heading = await screen.findByRole("heading", { name: "Administrator Email" });
    const section = heading.closest("section");
    expect(section).not.toBeNull();

    const controls = within(section as HTMLElement);
    await user.clear(controls.getByLabelText(/^New Email/));
    await user.type(controls.getByLabelText(/^New Email/), "NEW.ADMIN@Example.COM");
    await user.type(controls.getByLabelText(/^Current Password/), "correct horse battery staple");
    await user.click(controls.getByRole("button", { name: "Update Email and Sign In Again" }));

    await waitFor(() => expect(replace).toHaveBeenCalledWith("/login/?notice=credentials-updated"));
    const call = vi.mocked(fetch).mock.calls.find(([url]) => String(url).endsWith("/auth/email"));
    expect(call).toBeDefined();
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({
      email: "new.admin@example.com",
      current_password: "correct horse battery staple",
      totp_code: "",
    });
  });

  it("checks password confirmation before submitting and redirects after success", async () => {
    const user = userEvent.setup();
    renderSettings();
    const heading = await screen.findByRole("heading", { name: "Administrator Password" });
    const section = heading.closest("section");
    expect(section).not.toBeNull();

    const controls = within(section as HTMLElement);
    await user.type(controls.getByLabelText(/^Current Password/), "correct horse battery staple");
    await user.type(controls.getByLabelText(/^New Password/), "new correct horse battery staple");
    await user.type(controls.getByLabelText(/^Confirm New Password/), "different correct horse battery staple");
    await user.click(controls.getByRole("button", { name: "Update Password and Sign In Again" }));
    expect(await controls.findByRole("alert")).toHaveTextContent("do not match");
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).endsWith("/auth/password"))).toBe(false);

    await user.clear(controls.getByLabelText(/^Confirm New Password/));
    await user.type(controls.getByLabelText(/^Confirm New Password/), "new correct horse battery staple");
    await user.click(controls.getByRole("button", { name: "Update Password and Sign In Again" }));
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/login/?notice=credentials-updated"));
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).endsWith("/auth/password"))).toBe(true);
  });

  it("enrolls TOTP with a QR code and copyable recovery values", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText");
    renderSettings();

    await user.click(await screen.findByRole("button", { name: "Enable Two-Factor Authentication" }));
    const dialog = await screen.findByRole("dialog", { name: "Enable Two-Factor Authentication" });
    const controls = within(dialog);
    expect(controls.getByRole("img", { name: "Authenticator QR code" })).toBeVisible();
    expect(controls.getByText("JBSWY3DPEHPK3PXP")).toBeVisible();
    expect(controls.getByText(/^otpauth:\/\/totp\//)).toBeVisible();

    await user.click(controls.getByRole("button", { name: "Copy authenticator secret" }));
    await user.click(controls.getByRole("button", { name: "Copy provisioning URI" }));
    expect(writeText).toHaveBeenNthCalledWith(1, "JBSWY3DPEHPK3PXP");
    expect(writeText).toHaveBeenNthCalledWith(2, "otpauth://totp/GROK-GO:admin%40example.com?issuer=GROK-GO&secret=JBSWY3DPEHPK3PXP");

    await user.type(controls.getByLabelText(/^Verification Code/), "123456");
    await user.click(controls.getByRole("button", { name: "Confirm and Enable" }));

    await waitFor(() => expect(screen.getByRole("button", { name: "Disable Two-Factor Authentication" })).toBeVisible());
    const confirmCall = vi.mocked(fetch).mock.calls.find(([url]) => String(url).endsWith("/auth/totp/confirm"));
    expect(confirmCall).toBeDefined();
    expect(JSON.parse(String(confirmCall?.[1]?.body))).toEqual({ code: "123456" });
    expect(replace).not.toHaveBeenCalled();
  });

  it("requires the password and TOTP code to disable TOTP, then signs in again", async () => {
    totpEnabled = true;
    const user = userEvent.setup();
    renderSettings();

    await user.click(await screen.findByRole("button", { name: "Disable Two-Factor Authentication" }));
    const dialog = await screen.findByRole("dialog", { name: "Disable Two-Factor Authentication" });
    const controls = within(dialog);
    await user.type(controls.getByLabelText(/^Current Password/), "correct horse battery staple");
    await user.type(controls.getByLabelText(/^Two-Factor Code/), "654321");
    await user.click(controls.getByRole("button", { name: "Disable and Sign In Again" }));

    await waitFor(() => expect(replace).toHaveBeenCalledWith("/login/?notice=credentials-updated"));
    const disableCall = vi.mocked(fetch).mock.calls.find(([url]) => String(url).endsWith("/auth/totp/disable"));
    expect(disableCall).toBeDefined();
    expect(JSON.parse(String(disableCall?.[1]?.body))).toEqual({
      password: "correct horse battery staple",
      code: "654321",
    });
  });

  it("renders the TOTP controls in Chinese", async () => {
    window.localStorage.setItem("grok-go-locale", "zh");
    renderSettings();
    expect(await screen.findByRole("heading", { name: "双重验证" })).toBeVisible();
    expect(screen.getByRole("button", { name: "启用双重验证" })).toBeVisible();
  });

  it("shows pending restart state and submits only editable typed settings", async () => {
    const user = userEvent.setup();
    renderSettings();
    expect(await screen.findByText("Restart required")).toBeVisible();
    await user.click(screen.getByRole("tab", { name: "Limits" }));
    const concurrency = screen.getByLabelText("Global Concurrency");
    await user.clear(concurrency);
    await user.type(concurrency, "48");
    await user.click(screen.getByRole("button", { name: /^Save/ }));

    await waitFor(() => {
      const call = vi.mocked(fetch).mock.calls.find(([url, init]) => String(url).endsWith("/settings") && init?.method === "PUT");
      expect(call).toBeDefined();
      const payload = JSON.parse(String(call?.[1]?.body));
      expect(payload.max_concurrency).toBe(48);
      expect(payload).not.toHaveProperty("defaults");
      expect(payload).not.toHaveProperty("active");
      expect(payload).not.toHaveProperty("restart_required");
      expect(payload).not.toHaveProperty("default_rpm");
    });
  });
});
