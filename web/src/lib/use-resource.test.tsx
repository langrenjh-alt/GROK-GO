import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useResource } from "./use-resource";

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function dataResponse(value: string) {
  return new Response(JSON.stringify({ data: { value } }), { status: 200, headers: { "Content-Type": "application/json" } });
}

function ResourceHarness({ onReload }: { onReload?: (result: Promise<boolean>) => void }) {
  const resource = useResource<{ value: string }>("/ordered-resource", { value: "initial" });
  return (
    <div>
      <output aria-label="value">{resource.data.value}</output>
      <output aria-label="state">{resource.loading ? "loading" : resource.error?.message ?? "ready"}</output>
      <button type="button" onClick={() => {
        const result = resource.reload();
        onReload?.(result);
      }}>Reload</button>
    </div>
  );
}

describe("useResource", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("does not let an older response overwrite a newer reload", async () => {
    const firstRequest = deferred<Response>();
    const secondRequest = deferred<Response>();
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => firstRequest.promise)
      .mockImplementationOnce(() => secondRequest.promise);
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<ResourceHarness />);
    expect(screen.getByLabelText("state")).toHaveTextContent("loading");
    await user.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const firstCall = fetchMock.mock.calls[0] as unknown as [RequestInfo | URL, RequestInit?];
    expect(firstCall[1]?.signal?.aborted).toBe(true);

    await act(async () => {
      secondRequest.resolve(dataResponse("newer"));
    });
    expect(await screen.findByText("newer")).toBeVisible();
    expect(screen.getByLabelText("state")).toHaveTextContent("ready");

    await act(async () => {
      firstRequest.resolve(dataResponse("older"));
    });
    expect(screen.getByLabelText("value")).toHaveTextContent("newer");
    expect(screen.getByLabelText("state")).toHaveTextContent("ready");
  });

  it("aborts the active transport when the resource unmounts", async () => {
    const request = deferred<Response>();
    const fetchMock = vi.fn(() => request.promise);
    vi.stubGlobal("fetch", fetchMock);
    const view = render(<ResourceHarness />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const firstCall = fetchMock.mock.calls[0] as unknown as [RequestInfo | URL, RequestInit?];
    const signal = firstCall[1]?.signal;
    expect(signal?.aborted).toBe(false);

    view.unmount();
    expect(signal?.aborted).toBe(true);
  });

  it("keeps a superseded reload pending and reports the latest successful result", async () => {
    const initialRequest = deferred<Response>();
    const supersededRequest = deferred<Response>();
    const latestRequest = deferred<Response>();
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => initialRequest.promise)
      .mockImplementationOnce(() => supersededRequest.promise)
      .mockImplementationOnce(() => latestRequest.promise);
    vi.stubGlobal("fetch", fetchMock);
    const reloads: Promise<boolean>[] = [];
    const user = userEvent.setup();

    render(<ResourceHarness onReload={(result) => reloads.push(result)} />);
    await act(async () => {
      initialRequest.resolve(dataResponse("loaded"));
    });
    expect(await screen.findByText("loaded")).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Reload" }));
    await user.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(reloads).toHaveLength(2);

    let supersededSettled = false;
    void reloads[0].then(() => {
      supersededSettled = true;
    });
    await act(async () => {
      supersededRequest.reject(new Error("stale failure"));
    });
    expect(supersededSettled).toBe(false);
    expect(screen.getByLabelText("state")).toHaveTextContent("loading");

    await act(async () => {
      latestRequest.resolve(dataResponse("latest"));
    });
    await expect(reloads[1]).resolves.toBe(true);
    await expect(reloads[0]).resolves.toBe(true);
    expect(screen.getByLabelText("value")).toHaveTextContent("latest");
    expect(screen.getByLabelText("state")).toHaveTextContent("ready");
  });

  it("settles a superseded reload with the latest failure without waiting for its transport", async () => {
    const initialRequest = deferred<Response>();
    const supersededRequest = deferred<Response>();
    const latestRequest = deferred<Response>();
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => initialRequest.promise)
      .mockImplementationOnce(() => supersededRequest.promise)
      .mockImplementationOnce(() => latestRequest.promise);
    vi.stubGlobal("fetch", fetchMock);
    const reloads: Promise<boolean>[] = [];
    const user = userEvent.setup();

    render(<ResourceHarness onReload={(result) => reloads.push(result)} />);
    await act(async () => {
      initialRequest.resolve(dataResponse("loaded"));
    });
    expect(await screen.findByText("loaded")).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Reload" }));
    await user.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));

    await act(async () => {
      latestRequest.reject(new Error("latest failure"));
    });
    await expect(reloads[1]).resolves.toBe(false);
    await expect(reloads[0]).resolves.toBe(false);
    expect(screen.getByLabelText("state")).toHaveTextContent("latest failure");

    await act(async () => {
      supersededRequest.resolve(dataResponse("stale"));
    });
    expect(screen.getByLabelText("value")).toHaveTextContent("loaded");
    expect(screen.getByLabelText("state")).toHaveTextContent("latest failure");
  });
});
