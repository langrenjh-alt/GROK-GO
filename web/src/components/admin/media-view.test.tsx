import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@/components/i18n-provider";
import { ToastProvider } from "@/components/ui/toast";
import { MediaView } from "./media-view";

const resources = vi.hoisted(() => ({
  list: {
    data: { items: [
      { id: "media-1", kind: "image", content_type: "image/png", size: 1024, created_at: "2026-07-14T00:00:00Z", expires_at: "2026-07-15T00:00:00Z" },
      { id: "media-2", kind: "video", content_type: "video/mp4", size: 2048, created_at: "2026-07-14T00:00:00Z", expires_at: "2026-07-16T00:00:00Z" },
    ], total: 2 },
    loading: false,
    error: null,
    reload: vi.fn().mockResolvedValue(undefined),
  },
  summary: {
    data: { total_objects: 2, total_bytes: 3072, image_objects: 1, image_bytes: 1024, video_objects: 1, video_bytes: 2048, expiring_soon_objects: 1, expiring_soon_bytes: 1024, expiring_before: "2026-07-15T00:00:00Z" },
    loading: false,
    error: null,
    reload: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("@/lib/use-resource", () => ({
  useResource: (path: string) => path === "/media/summary" ? resources.summary : resources.list,
}));

function response(data: unknown) {
  return Promise.resolve(new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } }));
}

function renderMedia() {
  return render(<I18nProvider><ToastProvider><MediaView /></ToastProvider></I18nProvider>);
}

describe("MediaView maintenance", () => {
  beforeEach(() => {
    resources.list.reload.mockClear();
    resources.summary.reload.mockClear();
    window.localStorage.clear();
    vi.stubGlobal("fetch", vi.fn(() => response({ requested: 1, deleted: 1, deleted_bytes: 1024, failed: 0, errors: [] })));
  });

  afterEach(() => vi.unstubAllGlobals());

  it("renders the real cache summary", () => {
    renderMedia();
    const summary = screen.getByRole("region", { name: "媒体缓存汇总" });
    expect(within(summary).getByText("缓存对象")).toBeVisible();
    expect(within(summary).getByText("3.0 KB")).toBeVisible();
    expect(within(summary).getByText("24 小时内过期")).toBeVisible();
  });

  it("batch deletes only selected object IDs", async () => {
    const user = userEvent.setup();
    renderMedia();
    await user.click(screen.getByLabelText("选择 media-1"));
    await user.click(screen.getByRole("button", { name: "删除所选 (1)" }));
    const dialog = await screen.findByRole("dialog", { name: "删除所选媒体" });
    await user.click(within(dialog).getByRole("button", { name: "删除" }));

    await waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalledWith("/admin/api/media/batch-delete", expect.objectContaining({ method: "POST" })));
    const request = vi.mocked(fetch).mock.calls.find(([path]) => String(path).endsWith("/media/batch-delete"));
    expect(JSON.parse(String(request?.[1]?.body))).toEqual({ ids: ["media-1"] });
    expect(resources.summary.reload).toHaveBeenCalled();
  });

  it("cleans expired objects without requesting a full purge", async () => {
    const user = userEvent.setup();
    renderMedia();
    await user.click(screen.getByRole("button", { name: "清理过期" }));
    const dialog = await screen.findByRole("dialog", { name: "清理过期媒体" });
    await user.click(within(dialog).getByRole("button", { name: "开始清理" }));

    await waitFor(() => {
      const request = vi.mocked(fetch).mock.calls.find(([path]) => String(path).endsWith("/media/cleanup"));
      expect(JSON.parse(String(request?.[1]?.body))).toEqual({ mode: "expired" });
    });
  });

  it("requires an explicit phrase before clearing the entire cache", async () => {
    const user = userEvent.setup();
    renderMedia();
    await user.click(screen.getByRole("button", { name: "清空缓存" }));
    const dialog = await screen.findByRole("dialog", { name: "清空媒体缓存" });
    const remove = within(dialog).getByRole("button", { name: "删除" });
    expect(remove).toBeDisabled();
    await user.type(within(dialog).getByLabelText("输入 CLEAR 以确认"), "CLEAR");
    expect(remove).toBeEnabled();
    await user.click(remove);

    await waitFor(() => {
      const request = vi.mocked(fetch).mock.calls.find(([path]) => String(path).endsWith("/media/cleanup"));
      expect(JSON.parse(String(request?.[1]?.body))).toEqual({ mode: "all", confirm: true });
    });
  });
});
