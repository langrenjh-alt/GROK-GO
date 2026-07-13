"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { useTheme } from "next-themes";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import {
  Activity, Bot, Boxes, Bug, Check, ChevronDown, CircleGauge, FileClock,
  KeyRound, Languages, LogOut, Menu, Moon, Network, Settings, Sun, Users, X,
} from "lucide-react";
import { ApiError, apiFetch } from "@/lib/api";
import { cn } from "@/lib/cn";
import { useResource } from "@/lib/use-resource";
import { Button, IconButton } from "./ui/button";
import { Tooltip } from "./ui/tooltip";
import { useI18n, type MessageKey } from "./i18n-provider";

const navGroups: Array<{ label: MessageKey; items: Array<{ href: string; label: MessageKey; icon: typeof Activity }> }> = [
  { label: "nav.workspace", items: [
    { href: "/dashboard/", label: "nav.overview", icon: CircleGauge },
    { href: "/accounts/", label: "nav.accounts", icon: Users },
    { href: "/models/", label: "nav.models", icon: Bot },
    { href: "/keys/", label: "nav.keys", icon: KeyRound },
  ] },
  { label: "nav.operations", items: [
    { href: "/proxies/", label: "nav.proxies", icon: Network },
    { href: "/logs/", label: "nav.logs", icon: FileClock },
    { href: "/media/", label: "nav.media", icon: Boxes },
    { href: "/debugger/", label: "nav.debugger", icon: Bug },
  ] },
  { label: "nav.system", items: [{ href: "/settings/", label: "nav.settings", icon: Settings }] },
];

interface Principal {
  id: string;
  email: string;
  totp_enabled?: boolean;
}

type MeResponse = Principal | { principal: Principal };

function Brand() {
  const { t } = useI18n();
  return (
    <Link href="/dashboard/" className="flex h-14 items-center gap-2.5 border-b border-border px-3.5 outline-none transition-colors hover:bg-subtle focus-visible:shadow-focus">
      <span className="grid size-7 place-items-center rounded-[6px] bg-fg text-[13px] font-semibold text-bg">G</span>
      <span className="min-w-0"><strong className="block truncate text-label-14 font-semibold">GROK-GO</strong><span className="block truncate text-[11px] leading-3.5 text-fg-subtle">{t("brand.subtitle")}</span></span>
    </Link>
  );
}

function Navigation({ onNavigate }: { onNavigate?: () => void }) {
  const pathname = usePathname();
  const { t } = useI18n();
  return (
    <nav aria-label="Primary" className="scrollbar flex-1 overflow-y-auto px-2 py-3.5">
      {navGroups.map((group) => (
        <div key={group.label} className="mb-4 last:mb-0">
          <p className="mb-1 px-2 text-[11px] font-medium uppercase leading-5 text-fg-subtle">{t(group.label)}</p>
          <div className="grid gap-0.5">
            {group.items.map((item) => {
              const active = pathname === item.href || pathname === item.href.slice(0, -1);
              return (
                <Link key={item.href} href={item.href} onClick={onNavigate} aria-current={active ? "page" : undefined} className={cn("flex h-9 items-center gap-2.5 rounded-[6px] border border-transparent px-2 text-label-13 text-fg-muted outline-none transition-[background-color,border-color,color] hover:bg-subtle hover:text-fg focus-visible:shadow-focus", active && "border-border bg-subtle font-medium text-fg shadow-control") }>
                  <item.icon aria-hidden="true" className="size-4 shrink-0" /><span className="truncate">{t(item.label)}</span>
                </Link>
              );
            })}
          </div>
        </div>
      ))}
    </nav>
  );
}

function UserMenu({ principal }: { principal: Principal }) {
  const { theme, setTheme } = useTheme();
  const { locale, setLocale, t } = useI18n();
  const router = useRouter();
  const logout = async () => {
    try { await apiFetch("/auth/logout", { method: "POST" }); } finally { router.replace("/login/"); }
  };
  const itemClass = "control-text-sm flex h-8 w-full cursor-pointer items-center gap-2 rounded-[5px] px-2 text-left text-fg outline-none data-[highlighted]:bg-subtle data-[highlighted]:text-fg";
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger className="flex h-14 w-full items-center gap-2.5 border-t border-border px-3 text-left outline-none transition-colors hover:bg-subtle focus-visible:shadow-focus data-[state=open]:bg-subtle">
        <span className="grid size-7 shrink-0 place-items-center rounded-[6px] border border-blue-soft bg-blue-soft text-xs font-semibold uppercase text-blue">{principal.email.slice(0, 1) || "A"}</span>
        <span className="min-w-0 flex-1"><span className="block truncate text-label-13 font-medium text-fg">{locale === "zh" ? "管理员" : "Administrator"}</span><span className="block truncate text-[11px] text-fg-subtle">{principal.email}</span></span>
        <ChevronDown className="size-3.5 text-fg-subtle" />
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content side="top" align="start" sideOffset={7} className="z-[70] w-56 rounded-lg border border-border bg-surface p-1 shadow-menu">
          <DropdownMenu.Label className="px-2 py-1.5 text-[11px] font-medium uppercase text-fg-subtle">{t("theme.label")}</DropdownMenu.Label>
          {[{ id: "light", icon: Sun, key: "theme.light" }, { id: "dark", icon: Moon, key: "theme.dark" }, { id: "system", icon: Activity, key: "theme.system" }].map((item) => (
            <DropdownMenu.Item key={item.id} onSelect={() => setTheme(item.id)} className={itemClass}><item.icon className="size-3.5" />{t(item.key as MessageKey)}{theme === item.id ? <Check className="ml-auto size-3.5 text-blue" /> : null}</DropdownMenu.Item>
          ))}
          <DropdownMenu.Separator className="my-1 h-px bg-border" />
          <DropdownMenu.Item onSelect={() => setLocale(locale === "zh" ? "en" : "zh")} className={itemClass}><Languages className="size-3.5" />{locale === "zh" ? "English" : "简体中文"}</DropdownMenu.Item>
          <DropdownMenu.Item onSelect={() => void logout()} className={cn(itemClass, "text-danger")}><LogOut className="size-3.5" />{locale === "zh" ? "退出登录" : "Sign Out"}</DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function Sidebar({ principal, onNavigate }: { principal: Principal; onNavigate?: () => void }) {
  return <div className="flex h-full flex-col bg-surface"><Brand /><Navigation onNavigate={onNavigate} /><UserMenu principal={principal} /></div>;
}

export function AppShell({ children }: { children: React.ReactNode }) {
  const [mobileOpen, setMobileOpen] = useState(false);
  const pathname = usePathname();
  const router = useRouter();
  const { t } = useI18n();
  const auth = useResource<MeResponse>("/auth/me", { id: "", email: "" });
  const unauthorized = auth.error instanceof ApiError && auth.error.status === 401;
  const principal = "principal" in auth.data ? auth.data.principal : auth.data;
  const current = navGroups.flatMap((group) => group.items).find((item) => pathname === item.href || pathname === item.href.slice(0, -1));

  useEffect(() => {
    if (unauthorized) router.replace("/login/");
  }, [router, unauthorized]);

  if (auth.loading || unauthorized) return <AppShellSkeleton />;
  if (auth.error) return <AppShellError message={auth.error.message} onRetry={() => void auth.reload()} />;

  return (
    <div className="min-h-dvh bg-page text-fg lg:grid lg:grid-cols-[240px_minmax(0,1fr)]">
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-60 border-r border-border lg:block"><Sidebar principal={principal} /></aside>
      <div className="min-w-0 lg:col-start-2">
        <header className="sticky top-0 z-20 flex h-14 items-center border-b border-border bg-surface/95 px-3 backdrop-blur-md md:px-5">
          <div className="lg:hidden">
            <IconButton label={t("common.openMenu")} variant="tertiary" onClick={() => setMobileOpen(true)}><Menu className="size-4" /></IconButton>
          </div>
          <div className="ml-2 flex min-w-0 items-center gap-2 text-label-13 lg:ml-0"><span className="hidden text-fg-subtle sm:inline">GROK-GO</span><span className="hidden text-fg-subtle sm:inline">/</span><span className="truncate font-medium">{current ? t(current.label) : "Console"}</span></div>
          {current?.href !== "/debugger/" ? <Tooltip content={t("debugger.title")}><Link href="/debugger/" aria-label={t("debugger.title")} className="ml-auto inline-flex h-8 items-center gap-2 rounded-[6px] border border-border bg-surface px-2.5 text-label-13 font-medium text-fg shadow-control outline-none transition-colors hover:border-border-strong hover:bg-subtle focus-visible:shadow-focus"><Bug className="size-3.5" /><span className="hidden sm:inline">{t("debugger.title")}</span></Link></Tooltip> : null}
        </header>
        <main className="min-w-0">{children}</main>
      </div>
      <DialogPrimitive.Root open={mobileOpen} onOpenChange={setMobileOpen}>
        <DialogPrimitive.Portal>
          <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-black/40 data-[state=closed]:animate-fade-out data-[state=open]:animate-fade-in lg:hidden" />
          <DialogPrimitive.Content aria-label="Navigation" className="fixed inset-y-0 left-0 z-50 w-[min(86vw,288px)] border-r border-border bg-surface shadow-modal outline-none data-[state=closed]:animate-drawer-out data-[state=open]:animate-drawer-in lg:hidden">
            <DialogPrimitive.Close asChild><IconButton label={t("common.closeMenu")} variant="tertiary" className="absolute right-2.5 top-3 z-10"><X className="size-4" /></IconButton></DialogPrimitive.Close>
            <Sidebar principal={principal} onNavigate={() => setMobileOpen(false)} />
          </DialogPrimitive.Content>
        </DialogPrimitive.Portal>
      </DialogPrimitive.Root>
    </div>
  );
}

function AppShellSkeleton() {
  return (
    <div role="status" aria-label="Loading console" className="min-h-dvh bg-page text-fg lg:grid lg:grid-cols-[240px_minmax(0,1fr)]">
      <aside className="fixed inset-y-0 left-0 hidden w-60 border-r border-border bg-surface lg:block">
        <div className="flex h-14 items-center gap-2.5 border-b border-border px-4"><span className="size-7 animate-pulse rounded-[6px] bg-subtle" /><span className="h-4 w-24 animate-pulse rounded bg-subtle" /></div>
        <div className="grid gap-3 px-4 py-5">{Array.from({ length: 8 }, (_, index) => <span key={index} className="h-8 animate-pulse rounded-[6px] bg-subtle" />)}</div>
      </aside>
      <div className="min-w-0 lg:col-start-2">
        <div className="h-14 border-b border-border bg-surface" />
        <div className="px-4 py-6 sm:px-6 lg:px-8"><div className="h-8 w-48 animate-pulse rounded bg-subtle" /><div className="mt-3 h-4 w-full max-w-lg animate-pulse rounded bg-subtle" /></div>
        <div className="grid grid-cols-2 border-y border-border bg-surface sm:grid-cols-4">{Array.from({ length: 4 }, (_, index) => <div key={index} className="h-32 border-r border-border p-5"><div className="h-3 w-20 animate-pulse rounded bg-subtle" /><div className="mt-5 h-7 w-24 animate-pulse rounded bg-subtle" /></div>)}</div>
      </div>
    </div>
  );
}

function AppShellError({ message, onRetry }: { message: string; onRetry: () => void }) {
  const { locale, t } = useI18n();
  return (
    <div className="grid min-h-dvh place-items-center bg-page p-4">
      <section role="alert" className="w-full max-w-md rounded-lg border border-border bg-surface p-6 shadow-menu">
        <h1 className="text-heading-20 font-semibold">{locale === "zh" ? "控制台连接失败" : "Console Connection Failed"}</h1>
        <p className="mt-2 break-words text-copy-14 text-fg-muted">{message}</p>
        <Button className="mt-5" variant="secondary" onClick={onRetry}>{t("common.retry")}</Button>
      </section>
    </div>
  );
}
