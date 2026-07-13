import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@/components/i18n-provider";
import { DashboardView } from "./dashboard-view";

const dashboardResource = vi.hoisted(() => ({
  data: {
    requests_24h: 4,
    success_rate: 75,
    avg_latency_ms: 250,
    tokens_24h: 550,
    input_tokens_24h: 500,
    cached_tokens_24h: 320,
    usage_samples_24h: 3,
    cache_hit_rate: 64,
    active_accounts: 2,
    total_accounts: 3,
    active_keys: 1,
    gateway_healthy: true,
    hourly_requests: [...Array.from({ length: 23 }, () => 0), 4],
    hourly_input_tokens: [...Array.from({ length: 23 }, () => 0), 500],
    hourly_cached_tokens: [...Array.from({ length: 23 }, () => 0), 320],
    hourly_usage_samples: [...Array.from({ length: 23 }, () => 0), 3],
    hourly_cache_hit_rate: [...Array.from({ length: 23 }, () => 0), 64],
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

  it("shows the weighted cache hit rate and its token basis", () => {
    render(<I18nProvider><DashboardView /></I18nProvider>);

    expect(screen.getByText("缓存命中率")).toBeVisible();
    expect(screen.getByText("64.0%")).toBeVisible();
    expect(screen.getByText("320 / 500 输入 Token")).toBeVisible();
    expect(screen.getByRole("img", { name: "过去 24 小时缓存命中率趋势" })).toBeVisible();
    expect(screen.getByTitle("64.0% · 320 / 500 输入 Token")).toBeVisible();
    expect(screen.getByRole("link", { name: "查看日志" })).toHaveAttribute("href", "/logs");
  });

  it("localizes the cache metric in English", async () => {
    window.localStorage.setItem("grok-go-locale", "en");
    render(<I18nProvider><DashboardView /></I18nProvider>);

    expect(await screen.findByText("Cache Hit Rate")).toBeVisible();
    expect(screen.getByText("320 / 500 input tokens")).toBeVisible();
    expect(screen.getByRole("img", { name: "Cache hit rate over the last 24 hours" })).toBeVisible();
  });
});
