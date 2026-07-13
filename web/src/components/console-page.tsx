"use client";

import { AppShell } from "./app-shell";
import { DashboardView } from "./admin/dashboard-view";
import { AccountsView } from "./admin/accounts-view";
import { ProxiesView } from "./admin/proxies-view";
import { KeysView } from "./admin/keys-view";
import { ModelsView } from "./admin/models-view";
import { LogsView } from "./admin/logs-view";
import { MediaView } from "./admin/media-view";
import { SettingsView } from "./admin/settings-view";
import { DebuggerView } from "./admin/debugger-view";

export type ConsoleRoute = "dashboard" | "accounts" | "proxies" | "keys" | "models" | "logs" | "media" | "settings" | "debugger";

const views: Record<ConsoleRoute, React.ComponentType> = {
  dashboard: DashboardView,
  accounts: AccountsView,
  proxies: ProxiesView,
  keys: KeysView,
  models: ModelsView,
  logs: LogsView,
  media: MediaView,
  settings: SettingsView,
  debugger: DebuggerView,
};

export function ConsolePage({ route }: { route: ConsoleRoute }) {
  const View = views[route];
  return <AppShell><View /></AppShell>;
}
