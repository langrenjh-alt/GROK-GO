"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { useTheme } from "next-themes";
import {
  Check,
  ChevronLeft,
  ChevronRight,
  Database,
  KeyRound,
  Languages,
  LoaderCircle,
  LockKeyhole,
  Mail,
  Moon,
  ShieldCheck,
  Sun,
  UserRound,
} from "lucide-react";
import { apiFetch, jsonBody } from "@/lib/api";
import { cn } from "@/lib/cn";
import { useResource } from "@/lib/use-resource";
import { Button, IconButton } from "./ui/button";
import { Input } from "./ui/form";
import { useI18n } from "./i18n-provider";

interface StatusResponse {
  bootstrapped: boolean;
}

function AuthToolbar() {
  const { theme, setTheme } = useTheme();
  const { locale, setLocale } = useI18n();
  return (
    <div className="absolute right-4 top-4 z-10 flex items-center gap-1 rounded-[6px] border border-border bg-surface p-1 shadow-control">
      <IconButton label={locale === "zh" ? "浅色主题" : "Light theme"} variant={theme === "light" ? "secondary" : "tertiary"} onClick={() => setTheme("light")}><Sun className="size-4" /></IconButton>
      <IconButton label={locale === "zh" ? "深色主题" : "Dark theme"} variant={theme === "dark" ? "secondary" : "tertiary"} onClick={() => setTheme("dark")}><Moon className="size-4" /></IconButton>
      <span className="mx-1 h-5 w-px bg-border" />
      <IconButton label={locale === "zh" ? "Switch to English" : "切换到中文"} variant="tertiary" onClick={() => setLocale(locale === "zh" ? "en" : "zh")}><Languages className="size-4" /></IconButton>
    </div>
  );
}

function AuthBrand() {
  const { t } = useI18n();
  return (
    <div className="flex items-center gap-3">
      <span className="grid size-9 place-items-center rounded-lg bg-fg text-bg"><Database className="size-5" /></span>
      <div><strong className="block text-heading-16 font-semibold">GROK-GO</strong><span className="block text-copy-13 text-fg-muted">{t("brand.subtitle")}</span></div>
    </div>
  );
}

function BootstrapGuard({ page, children }: { page: "setup" | "login"; children: React.ReactNode }) {
  const router = useRouter();
  const { locale, t } = useI18n();
  const status = useResource<StatusResponse>("/status", { bootstrapped: false });
  const destination = !status.loading && !status.error
    ? page === "setup" && status.data.bootstrapped
      ? "/login/"
      : page === "login" && !status.data.bootstrapped
        ? "/setup/"
        : null
    : null;

  useEffect(() => {
    if (destination) router.replace(destination);
  }, [destination, router]);

  if (status.loading || destination) return <AuthLoading />;
  if (status.error) {
    return (
      <main className="relative grid min-h-dvh place-items-center bg-page px-4 py-20">
        <AuthToolbar />
        <div className="w-full max-w-[400px]">
          <div className="mb-8"><AuthBrand /></div>
          <section className="rounded-lg border border-border bg-surface p-6 shadow-menu">
            <h1 className="text-heading-20 font-semibold">{locale === "zh" ? "服务状态不可用" : "Service Status Unavailable"}</h1>
            <p role="alert" className="mt-2 text-copy-14 text-fg-muted">{status.error.message || t("common.requestFailed")}</p>
            <Button className="mt-5" variant="secondary" onClick={() => void status.reload()}>{t("common.retry")}</Button>
          </section>
        </div>
      </main>
    );
  }
  return children;
}

function AuthLoading() {
  const { t } = useI18n();
  return (
    <main className="relative grid min-h-dvh place-items-center bg-page px-4 py-20">
      <AuthToolbar />
      <div role="status" className="w-full max-w-[400px]">
        <div className="mb-8"><AuthBrand /></div>
        <div className="grid h-64 place-items-center rounded-lg border border-border bg-surface shadow-menu">
          <div className="flex items-center gap-2 text-copy-14 text-fg-muted"><LoaderCircle aria-hidden="true" className="size-4 animate-spin" />{t("common.loading")}</div>
        </div>
      </div>
    </main>
  );
}

export function LoginPage() {
  return <BootstrapGuard page="login"><LoginForm /></BootstrapGuard>;
}

function LoginForm() {
  const { t, locale } = useI18n();
  const router = useRouter();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    const queryNotice = new URLSearchParams(window.location.search).get("notice");
    let storedNotice = "";
    try {
      storedNotice = window.sessionStorage.getItem("grok-go-auth-notice") ?? "";
      window.sessionStorage.removeItem("grok-go-auth-notice");
    } catch {
      storedNotice = "";
    }
    if (queryNotice === "credentials-updated" || storedNotice === "credentials-updated") {
      // Surface the reason for the forced reauthentication after credential rotation.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setNotice(locale === "zh" ? "管理员登录信息已更新，请使用新凭据重新登录。" : "Administrator credentials were updated. Sign in again with the new credentials.");
      window.history.replaceState({}, "", "/login/");
    }
  }, [locale]);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError("");
    const form = new FormData(event.currentTarget);
    try {
      await apiFetch("/auth/login", {
        method: "POST",
        ...jsonBody({
          email: form.get("email"),
          password: form.get("password"),
          totp_code: String(form.get("totp_code") ?? "").trim(),
        }),
      });
      router.replace("/dashboard/");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setLoading(false);
    }
  }

  return (
    <main className="relative grid min-h-dvh place-items-center bg-page px-4 py-20">
      <AuthToolbar />
      <div className="w-full max-w-[400px]">
        <div className="mb-8"><AuthBrand /></div>
        <section aria-labelledby="login-heading" className="rounded-lg border border-border bg-surface shadow-menu">
          <div className="border-b border-border px-6 py-5">
            <h1 id="login-heading" className="text-heading-24 font-semibold">{t("login.title")}</h1>
            <p className="mt-1 text-copy-14 text-fg-muted">{locale === "zh" ? "使用管理员邮箱继续。" : "Continue with your administrator email."}</p>
          </div>
          <form onSubmit={submit}>
            <div className="grid gap-4 px-6 py-5">
              <Input name="email" type="email" label={locale === "zh" ? "邮箱" : "Email"} autoComplete="username" prefix={<Mail className="size-4" />} required autoFocus />
              <Input name="password" type="password" label={locale === "zh" ? "密码" : "Password"} autoComplete="current-password" prefix={<LockKeyhole className="size-4" />} required />
              <Input name="totp_code" label={locale === "zh" ? "双重验证码（可选）" : "Two-Factor Code (optional)"} inputMode="numeric" pattern="[0-9]*" maxLength={8} autoComplete="one-time-code" prefix={<ShieldCheck className="size-4" />} placeholder="123456" />
              {notice ? <p role="status" className="rounded-[6px] border border-green-soft bg-green-soft p-3 text-copy-13 text-green">{notice}</p> : null}
              {error ? <p role="alert" className="rounded-[6px] border border-red-soft bg-red-soft p-3 text-copy-13 text-danger">{error}</p> : null}
              <Button type="submit" size="large" loading={loading}>{locale === "zh" ? "登录" : "Sign In"}<ChevronRight className="size-4" /></Button>
            </div>
          </form>
        </section>
        <p className="mt-5 text-center text-copy-13 text-fg-subtle">{locale === "zh" ? "会话使用安全的同源 Cookie。" : "Sessions use secure same-origin cookies."}</p>
      </div>
    </main>
  );
}

interface SetupForm {
  bootstrap_token: string;
  email: string;
  password: string;
  confirm: string;
}

const setupInitial: SetupForm = { bootstrap_token: "", email: "", password: "", confirm: "" };

export function SetupPage() {
  return <BootstrapGuard page="setup"><SetupFormPage /></BootstrapGuard>;
}

function SetupFormPage() {
  const { t, locale } = useI18n();
  const router = useRouter();
  const [step, setStep] = useState(0);
  const [form, setForm] = useState(setupInitial);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const steps = [
    { icon: KeyRound, title: locale === "zh" ? "引导令牌" : "Bootstrap Token" },
    { icon: UserRound, title: locale === "zh" ? "管理员" : "Administrator" },
    { icon: Check, title: locale === "zh" ? "确认" : "Review" },
  ];

  function set<K extends keyof SetupForm>(key: K, value: SetupForm[K]) {
    setForm((current) => ({ ...current, [key]: value }));
  }

  function next() {
    setError("");
    if (step === 0 && !form.bootstrap_token.trim()) {
      setError(locale === "zh" ? "引导令牌为必填项。" : "Bootstrap token is required.");
      return;
    }
    if (step === 1) {
      if (!/^\S+@\S+\.\S+$/.test(form.email)) {
        setError(locale === "zh" ? "请输入有效的管理员邮箱。" : "Enter a valid administrator email.");
        return;
      }
      if (form.password.length < 12) {
        setError(locale === "zh" ? "密码至少需要 12 个字符。" : "Password must be at least 12 characters.");
        return;
      }
      if (form.password !== form.confirm) {
        setError(locale === "zh" ? "两次输入的密码不一致。" : "Passwords do not match.");
        return;
      }
    }
    setStep((current) => Math.min(2, current + 1));
  }

  async function finish() {
    setLoading(true);
    setError("");
    try {
      await apiFetch("/setup", {
        method: "POST",
        ...jsonBody({ bootstrap_token: form.bootstrap_token, email: form.email, password: form.password }),
      });
      router.replace("/login/");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("common.requestFailed"));
    } finally {
      setLoading(false);
    }
  }

  return (
    <main className="relative min-h-dvh bg-page px-4 py-16 sm:px-6">
      <AuthToolbar />
      <div className="mx-auto w-full max-w-4xl">
        <div className="mb-8"><AuthBrand /></div>
        <section aria-labelledby="setup-heading" className="grid overflow-hidden rounded-lg border border-border bg-surface shadow-menu md:grid-cols-[220px_minmax(0,1fr)]">
          <aside className="border-b border-border bg-subtle p-5 md:border-b-0 md:border-r">
            <h1 id="setup-heading" className="text-heading-20 font-semibold">{t("setup.title")}</h1>
            <ol className="mt-5 grid grid-cols-3 gap-2 md:grid-cols-1">
              {steps.map((item, index) => (
                <li key={item.title} className={cn("flex min-w-0 items-center gap-2.5 rounded-[6px] px-2 py-2 text-label-13 text-fg-muted", index === step && "bg-surface font-medium text-fg shadow-control", index < step && "text-green")}>
                  <span className="grid size-6 shrink-0 place-items-center rounded-full border border-border bg-surface">{index < step ? <Check className="size-3.5" /> : <item.icon className="size-3.5" />}</span>
                  <span className="hidden truncate sm:block">{item.title}</span>
                </li>
              ))}
            </ol>
          </aside>
          <div className="min-w-0">
            <div className="min-h-[430px] px-5 py-6 sm:px-8">
              {step === 0 ? <SetupToken form={form} set={set} locale={locale} /> : step === 1 ? <SetupAdmin form={form} set={set} locale={locale} /> : <SetupReview form={form} locale={locale} />}
              {error ? <p role="alert" className="mt-5 rounded-[6px] border border-red-soft bg-red-soft p-3 text-copy-13 text-danger">{error}</p> : null}
            </div>
            <div className="flex items-center justify-between border-t border-border px-5 py-4 sm:px-8">
              <Button variant="secondary" disabled={step === 0} onClick={() => setStep((current) => Math.max(0, current - 1))}><ChevronLeft className="size-4" />{locale === "zh" ? "上一步" : "Back"}</Button>
              {step < 2 ? <Button onClick={next}>{locale === "zh" ? "继续" : "Continue"}<ChevronRight className="size-4" /></Button> : <Button loading={loading} onClick={() => void finish()}>{locale === "zh" ? "完成设置" : "Finish Setup"}<Check className="size-4" /></Button>}
            </div>
          </div>
        </section>
      </div>
    </main>
  );
}

function StepIntro({ title, description }: { title: string; description: string }) {
  return <div className="mb-6"><h2 className="text-heading-20 font-semibold">{title}</h2><p className="mt-1 text-copy-14 text-fg-muted">{description}</p></div>;
}

function SetupToken({ form, set, locale }: { form: SetupForm; set: <K extends keyof SetupForm>(key: K, value: SetupForm[K]) => void; locale: string }) {
  return <><StepIntro title={locale === "zh" ? "验证引导令牌" : "Verify Bootstrap Token"} description={locale === "zh" ? "输入服务启动日志或部署环境提供的一次性令牌。" : "Enter the one-time token provided by deployment configuration or startup logs."} /><div className="grid max-w-md gap-4"><Input label={locale === "zh" ? "引导令牌" : "Bootstrap Token"} type="password" value={form.bootstrap_token} onChange={(event) => set("bootstrap_token", event.target.value)} autoComplete="off" prefix={<KeyRound className="size-4" />} required autoFocus /></div></>;
}

function SetupAdmin({ form, set, locale }: { form: SetupForm; set: <K extends keyof SetupForm>(key: K, value: SetupForm[K]) => void; locale: string }) {
  return <><StepIntro title={locale === "zh" ? "创建管理员" : "Create Administrator"} description={locale === "zh" ? "此邮箱账号拥有完整的控制台权限。" : "This email account has full console access."} /><div className="grid max-w-md gap-4"><Input label={locale === "zh" ? "管理员邮箱" : "Administrator Email"} type="email" value={form.email} onChange={(event) => set("email", event.target.value)} autoComplete="username" prefix={<Mail className="size-4" />} required /><Input label={locale === "zh" ? "密码" : "Password"} type="password" value={form.password} onChange={(event) => set("password", event.target.value)} description={locale === "zh" ? "至少 12 个字符。" : "At least 12 characters."} autoComplete="new-password" required /><Input label={locale === "zh" ? "确认密码" : "Confirm Password"} type="password" value={form.confirm} onChange={(event) => set("confirm", event.target.value)} autoComplete="new-password" required /></div></>;
}

function SetupReview({ form, locale }: { form: SetupForm; locale: string }) {
  return <><StepIntro title={locale === "zh" ? "确认设置" : "Review Settings"} description={locale === "zh" ? "管理员创建后，引导令牌将立即失效。" : "The bootstrap token becomes invalid immediately after the administrator is created."} /><dl className="divide-y divide-border border-y border-border">{[[locale === "zh" ? "引导令牌" : "Bootstrap Token", "••••••••••••"], [locale === "zh" ? "管理员邮箱" : "Administrator Email", form.email]].map(([label, value]) => <div key={label} className="grid gap-1 py-4 sm:grid-cols-[160px_1fr]"><dt className="text-label-13 text-fg-muted">{label}</dt><dd className="break-all text-label-14 font-medium">{value}</dd></div>)}</dl></>;
}
