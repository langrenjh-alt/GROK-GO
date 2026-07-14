import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@/components/i18n-provider";
import { DashboardView } from "./dashboard-view";

const dashboardResource = vi.hoisted(() => ({
  data: {
    requests_24h: 7,
    success_rate: 85.714,
    avg_latency_ms: 200,
    tokens_24h: 1160,
    input_tokens_24h: 400,
    cached_tokens_24h: 140,
    usage_samples_24h: 4,
    cache_samples_24h: 3,
    cache_request_hits_24h: 2,
    cache_eligible_requests_24h: 5,
    cache_hit_rate: 35,
    cache_token_reuse_rate: 35,
    cache_request_hit_rate: 66.6667,
    cache_usage_coverage: 80,
    active_accounts: 2,
    total_accounts: 3,
    active_keys: 1,
    gateway_healthy: true,
    hourly_requests: [...Array.from({ length: 23 }, () => 0), 7],
    hourly_input_tokens: [...Array.from({ length: 23 }, () => 0), 400],
    hourly_cached_tokens: [...Array.from({ length: 23 }, () => 0), 140],
    hourly_usage_samples: [...Array.from({ length: 23 }, () => 0), 4],
    hourly_cache_samples: [...Array.from({ length: 23 }, () => 0), 3],
    hourly_cache_hit_rate: [...Array.from({ length: 23 }, () => 0), 35],
    hourly_cache_token_reuse_rate: [...Array.from({ length: 23 }, () => 0), 35],
    recent_logs: [],
  },
  loading: false,
  error: null,
  reload: vi.fn(),
}));

vi.mock("@/lib/use-resource", () => ({
  useResource: () => dashboardResource,
}));

describe("DashboardView cache metrics", () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it("separates token reuse, request hits, and usage coverage", () => {
    render(<I18nProvider><DashboardView /></I18nProvider>);

    expect(screen.getByText("缓存 Token 复用率")).toBeVisible();
    expect(screen.getByText("35.0%")).toBeVisible();
    expect(screen.getByText("140 / 400 输入 Token")).toBeVisible();
    expect(screen.getByText("缓存请求命中率")).toBeVisible();
    expect(screen.getByText("66.7%")).toBeVisible();
    expect(screen.getByText("2 / 3 次请求")).toBeVisible();
    expect(screen.getByText("Usage 覆盖率")).toBeVisible();
    expect(screen.getByText("80.0%")).toBeVisible();
    expect(screen.getByText("4 / 5 次请求")).toBeVisible();
    expect(screen.getByRole("img", { name: "过去 24 小时缓存 Token 复用率趋势" })).toBeVisible();
    expect(screen.getByTitle("35.0% · 140 / 400 输入 Token")).toBeVisible();
    expect(screen.getByRole("link", { name: "查看日志" })).toHaveAttribute("href", "/logs");
  });

  it("localizes the cache metric in English", async () => {
    window.localStorage.setItem("grok-go-locale", "en");
    render(<I18nProvider><DashboardView /></I18nProvider>);

    expect(await screen.findByText("Cache Token Reuse Rate")).toBeVisible();
    expect(screen.getByText("140 / 400 input tokens")).toBeVisible();
    expect(screen.getByText("Request Hit Rate")).toBeVisible();
    expect(screen.getByText("Usage Coverage")).toBeVisible();
    expect(screen.getByRole("img", { name: "Cache token reuse rate over the last 24 hours" })).toBeVisible();
  });
});
