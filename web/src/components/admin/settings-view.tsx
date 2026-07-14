"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import * as Tabs from "@radix-ui/react-tabs";
import { QRCodeSVG } from "qrcode.react";
import { CircleAlert, Copy, LockKeyhole, Mail, RefreshCw, Save, ShieldCheck, ShieldOff } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button, IconButton } from "@/components/ui/button";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { Input, Switch } from "@/components/ui/form";
import { ErrorState, LoadingState } from "@/components/ui/feedback";
import { useToast } from "@/components/ui/toast";
import { useI18n } from "@/components/i18n-provider";
import { apiFetch, jsonBody } from "@/lib/api";
import { useResource } from "@/lib/use-resource";
import { ContentFrame, FormActions } from "./shared";

interface SettingsValues {
  public_base_url: string;
  request_timeout_seconds: number;
  max_request_bytes: number;
  max_concurrency: number;
  log_retention_days: number;
  cors_origins: string;
  trust_proxy_headers: boolean;
}

interface SettingsData extends SettingsValues {
  defaults: SettingsValues;
  active: SettingsValues;
  restart_required: string[];
}

interface AdminPrincipal {
  id: string;
  email: string;
  totp_enabled: boolean;
}

type MeResponse = AdminPrincipal | { principal: AdminPrincipal };

interface EmailForm {
  email: string;
  currentPassword: string;
  totpCode: string;
}

interface PasswordForm {
  currentPassword: string;
  newPassword: string;
  confirmation: string;
  totpCode: string;
}

interface TOTPEnrollmentWire {
  Secret?: string;
  URI?: string;
  ExpiresAt?: string;
  secret?: string;
  uri?: string;
  expires_at?: string;
}

interface TOTPEnrollment {
  secret: string;
  uri: string;
  expiresAt: string;
}

interface TOTPDisableForm {
  currentPassword: string;
  code: string;
}

const defaults: SettingsValues = { public_base_url: "http://127.0.0.1:8080", request_timeout_seconds: 120, max_request_bytes: 32 * 1024 * 1024, max_concurrency: 32, log_retention_days: 30, cors_origins: "", trust_proxy_headers: false };
const defaultResponse: SettingsData = { ...defaults, defaults, active: defaults, restart_required: [] };

function editableSettings(value: Partial<SettingsValues>): SettingsValues {
  return {
    public_base_url: value.public_base_url ?? defaults.public_base_url,
    request_timeout_seconds: value.request_timeout_seconds ?? defaults.request_timeout_seconds,
    max_request_bytes: value.max_request_bytes ?? defaults.max_request_bytes,
    max_concurrency: value.max_concurrency ?? defaults.max_concurrency,
    log_retention_days: value.log_retention_days ?? defaults.log_retention_days,
    cors_origins: value.cors_origins ?? defaults.cors_origins,
    trust_proxy_headers: value.trust_proxy_headers ?? defaults.trust_proxy_headers,
  };
}

export function SettingsView() {
  const { t, locale } = useI18n(); const { toast } = useToast(); const router = useRouter();
  const resource = useResource<SettingsData>("/settings", defaultResponse);
  const account = useResource<MeResponse>("/auth/me", { id: "", email: "", totp_enabled: false });
  const principal = "principal" in account.data ? account.data.principal : account.data;
  const [activeTab, setActiveTab] = useState("account");
  const [form, setForm] = useState<SettingsValues>(defaults); const [saving, setSaving] = useState(false);
  const [emailForm, setEmailForm] = useState<EmailForm>({ email: "", currentPassword: "", totpCode: "" });
  const [passwordForm, setPasswordForm] = useState<PasswordForm>({ currentPassword: "", newPassword: "", confirmation: "", totpCode: "" });
  const [emailSaving, setEmailSaving] = useState(false); const [passwordSaving, setPasswordSaving] = useState(false);
  const [emailError, setEmailError] = useState(""); const [passwordError, setPasswordError] = useState("");
  const [totpEnrollment, setTOTPEnrollment] = useState<TOTPEnrollment | null>(null);
  const [totpCode, setTOTPCode] = useState(""); const [totpError, setTOTPError] = useState("");
  const [totpBeginning, setTOTPBeginning] = useState(false); const [totpConfirming, setTOTPConfirming] = useState(false);
  const [totpDisableOpen, setTOTPDisableOpen] = useState(false);
  const [totpDisableForm, setTOTPDisableForm] = useState<TOTPDisableForm>({ currentPassword: "", code: "" });
  const [totpDisableError, setTOTPDisableError] = useState(""); const [totpDisabling, setTOTPDisabling] = useState(false);
  useEffect(() => {
    // Rehydrate the editable draft when the remote settings snapshot changes.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setForm(editableSettings(resource.data));
  }, [resource.data]);
  useEffect(() => {
    if (!principal.email) return;
    // Keep the draft synchronized with the authenticated principal snapshot.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setEmailForm((current) => ({ ...current, email: principal.email }));
  }, [principal.email]);
  function set<K extends keyof SettingsValues>(key: K, value: SettingsValues[K]) { setForm((current) => ({ ...current, [key]: value })); }
  async function save() {
    setSaving(true);
    try {
      const saved = await apiFetch<SettingsData>("/settings", { method: "PUT", ...jsonBody(form) });
      const restartPending = saved.restart_required?.length > 0;
      toast(restartPending ? (locale === "zh" ? "设置已保存，部分更改需重启服务" : "Settings saved. Some changes require a restart.") : (locale === "zh" ? "设置已保存并已生效" : "Settings saved and applied"));
      await resource.reload();
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setSaving(false);
    }
  }
  function finishCredentialChange() {
    try {
      window.sessionStorage.setItem("grok-go-auth-notice", "credentials-updated");
    } catch {
      // The query parameter still carries the notice when storage is disabled.
    }
    router.replace("/login/?notice=credentials-updated");
  }
  async function changeEmail(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const email = emailForm.email.trim().toLowerCase();
    setEmailError("");
    if (!/^\S+@\S+\.\S+$/.test(email)) {
      setEmailError(locale === "zh" ? "请输入有效的管理员邮箱。" : "Enter a valid administrator email.");
      return;
    }
    if (email === principal.email) {
      setEmailError(locale === "zh" ? "新邮箱需与当前邮箱不同。" : "The new email must differ from the current email.");
      return;
    }
    setEmailSaving(true);
    try {
      await apiFetch("/auth/email", { method: "PATCH", ...jsonBody({ email, current_password: emailForm.currentPassword, totp_code: emailForm.totpCode.trim() }) });
      toast(locale === "zh" ? "管理员邮箱已更新，请重新登录" : "Administrator email updated. Sign in again.");
      finishCredentialChange();
    } catch (reason) {
      setEmailError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setEmailSaving(false);
    }
  }
  async function changePassword(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPasswordError("");
    if (passwordForm.newPassword.length < 12) {
      setPasswordError(locale === "zh" ? "新密码至少需要 12 个字符。" : "The new password must contain at least 12 characters.");
      return;
    }
    if (passwordForm.newPassword.length > 4096) {
      setPasswordError(locale === "zh" ? "新密码不能超过 4096 个字符。" : "The new password cannot exceed 4096 characters.");
      return;
    }
    if (passwordForm.newPassword !== passwordForm.confirmation) {
      setPasswordError(locale === "zh" ? "两次输入的新密码不一致。" : "The new passwords do not match.");
      return;
    }
    if (passwordForm.newPassword === passwordForm.currentPassword) {
      setPasswordError(locale === "zh" ? "新密码需与当前密码不同。" : "The new password must differ from the current password.");
      return;
    }
    setPasswordSaving(true);
    try {
      await apiFetch("/auth/password", { method: "POST", ...jsonBody({ current_password: passwordForm.currentPassword, new_password: passwordForm.newPassword, confirm_password: passwordForm.confirmation, totp_code: passwordForm.totpCode.trim() }) });
      toast(locale === "zh" ? "管理员密码已更新，请重新登录" : "Administrator password updated. Sign in again.");
      finishCredentialChange();
    } catch (reason) {
      setPasswordError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setPasswordSaving(false);
    }
  }
  async function beginTOTP() {
    setTOTPBeginning(true);
    setTOTPError("");
    try {
      const result = await apiFetch<TOTPEnrollmentWire>("/auth/totp/begin", { method: "POST" });
      const enrollment = {
        secret: result.secret ?? result.Secret ?? "",
        uri: result.uri ?? result.URI ?? "",
        expiresAt: result.expires_at ?? result.ExpiresAt ?? "",
      };
      if (!enrollment.secret || !enrollment.uri) {
        throw new Error(locale === "zh" ? "服务器返回的双重验证配置不完整。" : "The server returned incomplete two-factor enrollment data.");
      }
      setTOTPCode("");
      setTOTPEnrollment(enrollment);
    } catch (reason) {
      toast(reason instanceof Error ? reason.message : t("common.requestFailed"), "error");
    } finally {
      setTOTPBeginning(false);
    }
  }
  async function copyTOTPValue(value: string, label: string) {
    try {
      await navigator.clipboard.writeText(value);
      toast(label);
    } catch {
      toast(locale === "zh" ? "复制失败，请手动选择内容。" : "Copy failed. Select the value manually.", "error");
    }
  }
  function setPrincipalTOTP(enabled: boolean) {
    account.setData((current) => "principal" in current
      ? { ...current, principal: { ...current.principal, totp_enabled: enabled } }
      : { ...current, totp_enabled: enabled });
  }
  async function confirmTOTP(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const code = totpCode.trim();
    setTOTPError("");
    if (!/^\d{6}$/.test(code)) {
      setTOTPError(locale === "zh" ? "请输入身份验证器显示的 6 位验证码。" : "Enter the 6-digit code from your authenticator.");
      return;
    }
    setTOTPConfirming(true);
    try {
      await apiFetch("/auth/totp/confirm", { method: "POST", ...jsonBody({ code }) });
      setPrincipalTOTP(true);
      setTOTPEnrollment(null);
      setTOTPCode("");
      toast(locale === "zh" ? "双重验证已启用" : "Two-factor authentication enabled");
      await account.reload();
    } catch (reason) {
      setTOTPError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setTOTPConfirming(false);
    }
  }
  function openTOTPDisable() {
    setTOTPDisableForm({ currentPassword: "", code: "" });
    setTOTPDisableError("");
    setTOTPDisableOpen(true);
  }
  async function disableTOTP(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const code = totpDisableForm.code.trim();
    setTOTPDisableError("");
    if (!totpDisableForm.currentPassword || !/^\d{6}$/.test(code)) {
      setTOTPDisableError(locale === "zh" ? "请输入当前密码和 6 位验证码。" : "Enter your current password and 6-digit code.");
      return;
    }
    setTOTPDisabling(true);
    try {
      await apiFetch("/auth/totp/disable", { method: "POST", ...jsonBody({ password: totpDisableForm.currentPassword, code }) });
      setPrincipalTOTP(false);
      toast(locale === "zh" ? "双重验证已停用，请重新登录" : "Two-factor authentication disabled. Sign in again.");
      finishCredentialChange();
    } catch (reason) {
      setTOTPDisableError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setTOTPDisabling(false);
    }
  }
  const tabClass = "h-9 whitespace-nowrap border-b-2 border-transparent px-3 text-label-14 text-fg-muted outline-none hover:text-fg focus-visible:shadow-focus data-[state=active]:border-fg data-[state=active]:font-medium data-[state=active]:text-fg";
  if ((resource.loading && !resource.data.public_base_url) || (account.loading && !principal.email)) return <><PageHeader title={t("settings.title")} description={t("settings.description")} /><LoadingState label={t("common.loading")} /></>;
  return <ContentFrame>
    <PageHeader title={t("settings.title")} description={t("settings.description")} actions={<><Button type="button" size="small" variant="secondary" onClick={() => void Promise.all([resource.reload(), account.reload()])}><RefreshCw className="size-3.5" />{t("common.refresh")}</Button>{activeTab !== "account" ? <Button type="button" size="small" loading={saving} onClick={() => void save()}><Save className="size-3.5" />{t("common.save")}</Button> : null}</>} />
    {resource.error ? <div className="px-4 pb-4 sm:px-6 lg:px-8"><ErrorState title={t("common.requestFailed")} description={resource.error.message} onRetry={() => void resource.reload()} /></div> : null}
    {account.error ? <div className="px-4 pb-4 sm:px-6 lg:px-8"><ErrorState title={t("common.requestFailed")} description={account.error.message} onRetry={() => void account.reload()} /></div> : null}
    {resource.data.restart_required?.length ? <RestartNotice fields={resource.data.restart_required} locale={locale} /> : null}
    <Tabs.Root value={activeTab} onValueChange={setActiveTab}>
      <Tabs.List aria-label={locale === "zh" ? "设置分类" : "Settings sections"} className="scrollbar flex overflow-x-auto border-y border-border bg-surface px-4 sm:px-6 lg:px-8">
        <Tabs.Trigger value="account" className={tabClass}>{locale === "zh" ? "管理员" : "Administrator"}</Tabs.Trigger>
        <Tabs.Trigger value="general" className={tabClass}>{locale === "zh" ? "常规" : "General"}</Tabs.Trigger>
        <Tabs.Trigger value="limits" className={tabClass}>{locale === "zh" ? "限制" : "Limits"}</Tabs.Trigger>
        <Tabs.Trigger value="storage" className={tabClass}>{locale === "zh" ? "存储" : "Storage"}</Tabs.Trigger>
        <Tabs.Trigger value="security" className={tabClass}>{locale === "zh" ? "安全" : "Security"}</Tabs.Trigger>
      </Tabs.List>
      <Tabs.Content value="account" className="outline-none">
        <SettingsSection title={locale === "zh" ? "管理员邮箱" : "Administrator Email"} description={locale === "zh" ? "邮箱是控制台的登录身份。更新后所有管理会话都会结束。" : "The email is the console sign-in identity. Updating it ends every administrator session."}>
          <form className="grid gap-4" onSubmit={(event) => void changeEmail(event)}>
            <Input type="email" label={locale === "zh" ? "新邮箱" : "New Email"} value={emailForm.email} onChange={(event) => setEmailForm((current) => ({ ...current, email: event.target.value }))} autoComplete="email" prefix={<Mail className="size-4" />} required />
            <Input type="password" label={locale === "zh" ? "当前密码" : "Current Password"} value={emailForm.currentPassword} onChange={(event) => setEmailForm((current) => ({ ...current, currentPassword: event.target.value }))} autoComplete="current-password" prefix={<LockKeyhole className="size-4" />} required />
            {principal.totp_enabled ? <Input label={locale === "zh" ? "双重验证码" : "Two-Factor Code"} value={emailForm.totpCode} onChange={(event) => setEmailForm((current) => ({ ...current, totpCode: event.target.value }))} inputMode="numeric" pattern="[0-9]*" maxLength={8} autoComplete="one-time-code" prefix={<ShieldCheck className="size-4" />} required /> : null}
            {emailError ? <CredentialError message={emailError} /> : null}
            <div><Button type="submit" loading={emailSaving}>{locale === "zh" ? "更新邮箱并重新登录" : "Update Email and Sign In Again"}</Button></div>
          </form>
        </SettingsSection>
        <SettingsSection title={locale === "zh" ? "管理员密码" : "Administrator Password"} description={locale === "zh" ? "密码至少 12 个字符。更新后所有管理会话都会结束。" : "Passwords require at least 12 characters. Updating it ends every administrator session."}>
          <form className="grid gap-4" onSubmit={(event) => void changePassword(event)}>
            <Input type="password" label={locale === "zh" ? "当前密码" : "Current Password"} value={passwordForm.currentPassword} onChange={(event) => setPasswordForm((current) => ({ ...current, currentPassword: event.target.value }))} autoComplete="current-password" prefix={<LockKeyhole className="size-4" />} required />
            <Input type="password" label={locale === "zh" ? "新密码" : "New Password"} value={passwordForm.newPassword} onChange={(event) => setPasswordForm((current) => ({ ...current, newPassword: event.target.value }))} autoComplete="new-password" minLength={12} maxLength={4096} prefix={<LockKeyhole className="size-4" />} required />
            <Input type="password" label={locale === "zh" ? "确认新密码" : "Confirm New Password"} value={passwordForm.confirmation} onChange={(event) => setPasswordForm((current) => ({ ...current, confirmation: event.target.value }))} autoComplete="new-password" minLength={12} maxLength={4096} prefix={<LockKeyhole className="size-4" />} required />
            {principal.totp_enabled ? <Input label={locale === "zh" ? "双重验证码" : "Two-Factor Code"} value={passwordForm.totpCode} onChange={(event) => setPasswordForm((current) => ({ ...current, totpCode: event.target.value }))} inputMode="numeric" pattern="[0-9]*" maxLength={8} autoComplete="one-time-code" prefix={<ShieldCheck className="size-4" />} required /> : null}
            {passwordError ? <CredentialError message={passwordError} /> : null}
            <div><Button type="submit" loading={passwordSaving}>{locale === "zh" ? "更新密码并重新登录" : "Update Password and Sign In Again"}</Button></div>
          </form>
        </SettingsSection>
        <SettingsSection title={locale === "zh" ? "双重验证" : "Two-Factor Authentication"} description={locale === "zh" ? "使用基于时间的一次性密码保护管理员登录。" : "Protect administrator sign-in with a time-based one-time password."}>
          <div className="flex flex-col gap-4 border-y border-border py-4 sm:flex-row sm:items-center">
            <ShieldCheck aria-hidden="true" className={principal.totp_enabled ? "size-5 shrink-0 text-green" : "size-5 shrink-0 text-fg-subtle"} />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <p className="text-label-14 font-medium text-fg">{locale === "zh" ? "身份验证器应用" : "Authenticator App"}</p>
                <Badge tone={principal.totp_enabled ? "green" : "neutral"} dot>{principal.totp_enabled ? t("common.enabled") : t("common.disabled")}</Badge>
              </div>
              <p className="mt-1 text-copy-13 text-fg-muted">{principal.totp_enabled ? (locale === "zh" ? "登录及修改管理员凭据时需要动态验证码。" : "A current code is required for sign-in and administrator credential changes.") : (locale === "zh" ? "支持兼容 TOTP 的身份验证器。" : "Works with TOTP-compatible authenticator apps.")}</p>
            </div>
            {principal.totp_enabled
              ? <Button type="button" variant="secondary" onClick={openTOTPDisable}><ShieldOff className="size-3.5" />{locale === "zh" ? "停用双重验证" : "Disable Two-Factor Authentication"}</Button>
              : <Button type="button" loading={totpBeginning} onClick={() => void beginTOTP()}><ShieldCheck className="size-3.5" />{locale === "zh" ? "启用双重验证" : "Enable Two-Factor Authentication"}</Button>}
          </div>
        </SettingsSection>
      </Tabs.Content>
      <Tabs.Content value="general" className="outline-none">
        <SettingsSection title={locale === "zh" ? "服务地址" : "Service Address"} description={locale === "zh" ? "用于生成公开媒体端点的服务地址。" : "Service address used to generate public media endpoints."}>
          <Input type="url" label={locale === "zh" ? "公共基础 URL" : "Public Base URL"} value={form.public_base_url} onChange={(event) => set("public_base_url", event.target.value)} placeholder="https://api.example.com" description={locale === "zh" ? `用于签名媒体链接。默认值：${resource.data.defaults?.public_base_url || "未设置"}。更改后需重启。` : `Used for signed media links. Default: ${resource.data.defaults?.public_base_url || "not set"}. Requires restart.`} required />
        </SettingsSection>
      </Tabs.Content>
      <Tabs.Content value="limits" className="outline-none">
        <SettingsSection title={locale === "zh" ? "网关限制" : "Gateway Limits"} description={locale === "zh" ? "这些值作为未覆盖客户端的默认值。" : "These values are defaults for clients without overrides."}>
          <div className="grid gap-4 sm:grid-cols-2"><Input type="number" min="1" max="3600" label={locale === "zh" ? "请求超时（秒）" : "Request Timeout (seconds)"} value={form.request_timeout_seconds} onChange={(event) => set("request_timeout_seconds", Number(event.target.value))} description={locale === "zh" ? "1–3600 秒，保存后对新上游请求生效。" : "1–3600 seconds; applies to new upstream requests."} /><Input type="number" min={1024 * 1024} max={1024 * 1024 * 1024} label={locale === "zh" ? "最大请求体（字节）" : "Maximum Request Body (bytes)"} value={form.max_request_bytes} onChange={(event) => set("max_request_bytes", Number(event.target.value))} description={locale === "zh" ? "1 MiB–1 GiB，管理接口和网关共用此限制。" : "1 MiB–1 GiB, shared by admin and gateway endpoints."} /></div>
          <Input type="number" min="1" max="1000000" label={locale === "zh" ? "全局最大并发" : "Global Concurrency"} value={form.max_concurrency} onChange={(event) => set("max_concurrency", Number(event.target.value))} description={locale === "zh" ? "整个进程允许的同时进行中网关请求数，最高 1,000,000。" : "Maximum in-flight gateway requests for this process, up to 1,000,000."} />
        </SettingsSection>
      </Tabs.Content>
      <Tabs.Content value="storage" className="outline-none">
        <SettingsSection title={locale === "zh" ? "保留策略" : "Retention Policy"} description={locale === "zh" ? "过期对象由后台清理任务删除。" : "Expired objects are removed by the background cleanup job."}>
          <Input type="number" min="1" max="3650" label={locale === "zh" ? "日志保留（天）" : "Log Retention (days)"} value={form.log_retention_days} onChange={(event) => set("log_retention_days", Number(event.target.value))} description={locale === "zh" ? "保存后立即触发一次过期日志清理。" : "Saving immediately triggers an expired-log cleanup pass."} />
        </SettingsSection>
      </Tabs.Content>
      <Tabs.Content value="security" className="outline-none">
        <SettingsSection title={locale === "zh" ? "跨域与代理" : "Cross-Origin and Proxy"} description={locale === "zh" ? "限制浏览器跨域访问，并配置可信入口代理。" : "Control browser origins and trusted ingress behavior."}>
          <Input label="CORS Origins" value={form.cors_origins} onChange={(event) => set("cors_origins", event.target.value)} placeholder="https://app.example.com" description={locale === "zh" ? "多个来源使用英文逗号分隔。" : "Separate multiple origins with commas."} />
          <div className="divide-y divide-border border-y border-border">
            <div className="py-4"><Switch label={locale === "zh" ? "信任代理请求头" : "Trust Proxy Headers"} description={locale === "zh" ? "仅在受控反向代理之后启用；更改后需重启。" : "Enable only behind a controlled reverse proxy; requires restart."} checked={form.trust_proxy_headers} onCheckedChange={(value) => set("trust_proxy_headers", value)} /></div>
          </div>
        </SettingsSection>
      </Tabs.Content>
    </Tabs.Root>
    <Dialog open={Boolean(totpEnrollment)} onOpenChange={(open) => { if (!open && !totpConfirming) { setTOTPEnrollment(null); setTOTPCode(""); setTOTPError(""); } }}>
      <DialogContent title={locale === "zh" ? "启用双重验证" : "Enable Two-Factor Authentication"} description={locale === "zh" ? `为 ${principal.email} 绑定身份验证器。` : `Connect an authenticator to ${principal.email}.`} className="max-w-2xl">
        {totpEnrollment ? <form onSubmit={(event) => void confirmTOTP(event)}>
          <div className="grid gap-5 px-5 py-5 sm:grid-cols-[184px_minmax(0,1fr)]">
            <div role="img" aria-label={locale === "zh" ? "身份验证器二维码" : "Authenticator QR code"} className="mx-auto grid aspect-square w-full max-w-[184px] place-items-center self-start rounded-[6px] border border-border bg-white p-3 shadow-control">
              <QRCodeSVG aria-hidden="true" value={totpEnrollment.uri} size={160} level="M" bgColor="#ffffff" fgColor="#000000" className="h-auto w-full" />
            </div>
            <div className="grid min-w-0 gap-4">
              <TOTPValue label={locale === "zh" ? "身份验证器密钥" : "Authenticator Secret"} value={totpEnrollment.secret} copyLabel={locale === "zh" ? "复制身份验证器密钥" : "Copy authenticator secret"} onCopy={() => void copyTOTPValue(totpEnrollment.secret, locale === "zh" ? "身份验证器密钥已复制" : "Authenticator secret copied")} />
              <TOTPValue label={locale === "zh" ? "配置 URI" : "Provisioning URI"} value={totpEnrollment.uri} copyLabel={locale === "zh" ? "复制配置 URI" : "Copy provisioning URI"} onCopy={() => void copyTOTPValue(totpEnrollment.uri, locale === "zh" ? "配置 URI 已复制" : "Provisioning URI copied")} />
              {totpEnrollment.expiresAt ? <p className="text-copy-13 text-fg-muted">{locale === "zh" ? "此配置将在 " : "This enrollment expires "}<time dateTime={totpEnrollment.expiresAt}>{formatEnrollmentExpiry(totpEnrollment.expiresAt, locale)}</time>{locale === "zh" ? " 失效。" : "."}</p> : null}
              <Input label={locale === "zh" ? "验证码" : "Verification Code"} value={totpCode} onChange={(event) => setTOTPCode(event.target.value.replace(/\D/g, "").slice(0, 6))} inputMode="numeric" pattern="[0-9]{6}" maxLength={6} autoComplete="one-time-code" prefix={<ShieldCheck className="size-4" />} description={locale === "zh" ? "输入身份验证器当前显示的 6 位验证码。" : "Enter the current 6-digit code from your authenticator."} error={totpError || undefined} required />
            </div>
          </div>
          <FormActions><Button type="button" variant="secondary" disabled={totpConfirming} onClick={() => { setTOTPEnrollment(null); setTOTPCode(""); setTOTPError(""); }}>{t("common.cancel")}</Button><Button type="submit" loading={totpConfirming}>{locale === "zh" ? "确认并启用" : "Confirm and Enable"}</Button></FormActions>
        </form> : null}
      </DialogContent>
    </Dialog>
    <Dialog open={totpDisableOpen} onOpenChange={(open) => { if (!totpDisabling) setTOTPDisableOpen(open); }}>
      <DialogContent title={locale === "zh" ? "停用双重验证" : "Disable Two-Factor Authentication"} description={locale === "zh" ? "停用后所有管理员会话都会结束。" : "Disabling two-factor authentication ends every administrator session."}>
        <form onSubmit={(event) => void disableTOTP(event)}>
          <div className="grid gap-4 px-5 py-5">
            <Input type="password" label={locale === "zh" ? "当前密码" : "Current Password"} value={totpDisableForm.currentPassword} onChange={(event) => setTOTPDisableForm((current) => ({ ...current, currentPassword: event.target.value }))} autoComplete="current-password" prefix={<LockKeyhole className="size-4" />} required />
            <Input label={locale === "zh" ? "双重验证码" : "Two-Factor Code"} value={totpDisableForm.code} onChange={(event) => setTOTPDisableForm((current) => ({ ...current, code: event.target.value.replace(/\D/g, "").slice(0, 6) }))} inputMode="numeric" pattern="[0-9]{6}" maxLength={6} autoComplete="one-time-code" prefix={<ShieldCheck className="size-4" />} required />
            {totpDisableError ? <CredentialError message={totpDisableError} /> : null}
          </div>
          <FormActions><Button type="button" variant="secondary" disabled={totpDisabling} onClick={() => setTOTPDisableOpen(false)}>{t("common.cancel")}</Button><Button type="submit" variant="danger" loading={totpDisabling}>{locale === "zh" ? "停用并重新登录" : "Disable and Sign In Again"}</Button></FormActions>
        </form>
      </DialogContent>
    </Dialog>
  </ContentFrame>;
}

function TOTPValue({ label, value, copyLabel, onCopy }: { label: string; value: string; copyLabel: string; onCopy: () => void }) {
  return <div className="grid min-w-0 gap-1.5"><p className="text-label-13 font-medium text-fg">{label}</p><div className="flex min-w-0 items-center gap-2 rounded-[6px] border border-border bg-subtle p-2 pl-3"><code className="min-w-0 flex-1 break-all text-label-13 text-fg">{value}</code><IconButton type="button" label={copyLabel} variant="secondary" onClick={onCopy}><Copy className="size-4" /></IconButton></div></div>;
}

function formatEnrollmentExpiry(value: string, locale: "zh" | "en") {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return parsed.toLocaleString(locale === "zh" ? "zh-CN" : "en-US", { dateStyle: "medium", timeStyle: "short" });
}

function SettingsSection({ title, description, children }: { title: string; description: string; children: React.ReactNode }) {
  return <section className="grid gap-6 border-b border-border bg-surface px-4 py-6 sm:grid-cols-[220px_minmax(0,560px)] sm:px-6 lg:px-8"><div><h2 className="text-heading-16 font-semibold">{title}</h2><p className="mt-1 text-copy-13 text-fg-muted">{description}</p></div><div className="grid gap-5">{children}</div></section>;
}

function CredentialError({ message }: { message: string }) {
  return <p role="alert" className="rounded-[6px] border border-red-soft bg-red-soft p-3 text-copy-13 text-danger">{message}</p>;
}

function RestartNotice({ fields, locale }: { fields: string[]; locale: "zh" | "en" }) {
  const labels: Record<string, [string, string]> = {
    public_base_url: ["公共基础 URL", "Public Base URL"],
    trust_proxy_headers: ["代理请求头信任", "Trust Proxy Headers"],
  };
  const names = fields.map((field) => labels[field]?.[locale === "zh" ? 0 : 1] ?? field).join(locale === "zh" ? "、" : ", ");
  const englishVerb = fields.length === 1 ? "is" : "are";
  return <div role="status" className="mx-4 mb-4 flex items-start gap-3 rounded-[6px] border border-amber-soft bg-amber-soft px-3 py-2.5 text-copy-13 text-fg sm:mx-6 lg:mx-8"><CircleAlert className="mt-0.5 size-4 shrink-0 text-amber" /><div><p className="font-medium">{locale === "zh" ? "存在待重启设置" : "Restart required"}</p><p className="mt-0.5 text-fg-muted">{locale === "zh" ? `${names} 已保存，当前进程仍使用旧值。重启服务后生效。` : `${names} ${englishVerb} saved while this process continues using the previous value. Restart the service to apply the change.`}</p></div></div>;
}
