"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { Locale } from "@/lib/types";

const messages = {
  "brand.subtitle": ["Grok API 控制台", "Grok API Console"],
  "nav.overview": ["概览", "Overview"],
  "nav.accounts": ["账号池", "Accounts"],
  "nav.proxies": ["代理节点", "Proxies"],
  "nav.keys": ["访问密钥", "API Keys"],
  "nav.models": ["模型路由", "Models"],
  "nav.logs": ["请求日志", "Request Logs"],
  "nav.media": ["媒体缓存", "Media"],
  "nav.debugger": ["接口调试", "Debugger"],
  "nav.settings": ["系统设置", "Settings"],
  "nav.workspace": ["工作区", "Workspace"],
  "nav.operations": ["运维", "Operations"],
  "nav.system": ["系统", "System"],
  "common.search": ["搜索", "Search"],
  "common.refresh": ["刷新", "Refresh"],
  "common.add": ["新建", "Add"],
  "common.save": ["保存更改", "Save Changes"],
  "common.cancel": ["取消", "Cancel"],
  "common.delete": ["删除", "Delete"],
  "common.close": ["关闭", "Close"],
  "common.enabled": ["已启用", "Enabled"],
  "common.disabled": ["已停用", "Disabled"],
  "common.status": ["状态", "Status"],
  "common.name": ["名称", "Name"],
  "common.actions": ["操作", "Actions"],
  "common.loading": ["正在载入", "Loading"],
  "common.retry": ["重试", "Retry"],
  "common.none": ["暂无数据", "No data"],
  "common.copied": ["已复制到剪贴板", "Copied to clipboard"],
  "common.requestFailed": ["请求失败，请检查服务状态。", "Request failed. Check the service status."],
  "common.openMenu": ["打开导航", "Open menu"],
  "common.closeMenu": ["关闭导航", "Close menu"],
  "theme.label": ["显示主题", "Display theme"],
  "theme.light": ["浅色", "Light"],
  "theme.dark": ["深色", "Dark"],
  "theme.system": ["跟随系统", "System"],
  "locale.label": ["界面语言", "Interface language"],
  "account.title": ["账号池", "Accounts"],
  "account.description": ["管理上游凭据、配额、优先级与账号健康状态。", "Manage upstream credentials, quotas, priority, and account health."],
  "proxy.title": ["代理节点", "Proxies"],
  "proxy.description": ["配置账号出口代理并查看最近一次健康检查。", "Configure account egress proxies and inspect their latest health checks."],
  "key.title": ["访问密钥", "API Keys"],
  "key.description": ["签发客户端密钥并限制速率、并发与配额。", "Issue client keys and enforce rate, concurrency, and quota limits."],
  "model.title": ["模型路由", "Model Routing"],
  "model.description": ["控制公开模型、上游映射、能力与凭据要求。", "Control public models, upstream mappings, capabilities, and credential requirements."],
  "log.title": ["请求日志", "Request Logs"],
  "log.description": ["检索请求结果、延迟、Token 用量与错误摘要。", "Inspect request outcomes, latency, token usage, and error summaries."],
  "media.title": ["媒体缓存", "Media Cache"],
  "media.description": ["查看图片与视频产物的大小、类型和过期时间。", "Review image and video artifacts, sizes, types, and expiration."],
  "settings.title": ["系统设置", "Settings"],
  "settings.description": ["配置公共端点、安全策略、存储和运行参数。", "Configure public endpoints, security policy, storage, and runtime behavior."],
  "debugger.title": ["接口调试", "API Debugger"],
  "debugger.description": ["构造请求并查看网关返回的状态、响应头和 JSON。", "Compose a request and inspect gateway status, headers, and JSON output."],
  "dashboard.title": ["运行概览", "Runtime Overview"],
  "dashboard.description": ["实时查看流量、账号容量与网关健康状况。", "Monitor traffic, account capacity, and gateway health."],
  "setup.title": ["初始化 GROK-GO", "Set Up GROK-GO"],
  "login.title": ["登录控制台", "Sign In"],
} as const;

export type MessageKey = keyof typeof messages;

interface I18nValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: MessageKey) => string;
}

const I18nContext = createContext<I18nValue | null>(null);

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>("zh");
  useEffect(() => {
    const saved = window.localStorage.getItem("grok-go-locale");
    // Restore the persisted external preference after hydration.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (saved === "zh" || saved === "en") setLocaleState(saved);
  }, []);
  const setLocale = useCallback((next: Locale) => {
    setLocaleState(next);
    window.localStorage.setItem("grok-go-locale", next);
    document.documentElement.lang = next === "zh" ? "zh-CN" : "en";
  }, []);
  const value = useMemo<I18nValue>(
    () => ({
      locale,
      setLocale,
      t: (key) => messages[key][locale === "zh" ? 0 : 1],
    }),
    [locale, setLocale],
  );
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nValue {
  const value = useContext(I18nContext);
  if (!value) throw new Error("useI18n must be used within I18nProvider");
  return value;
}
