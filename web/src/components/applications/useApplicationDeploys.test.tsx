// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { focusManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { buildAppDeploys, type AppSourceRef, type AppWorkload } from "@skyhook-io/k8s-ui";
import { MAX_WORKLOADS_READ, useApplicationDeploys } from "./useApplicationDeploys";

vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);

const deployment: AppWorkload = {
  kind: "Deployment",
  namespace: "shop",
  name: "checkout-api-production-primary-us-east1-b",
  health: "healthy",
  ready: 2,
  desired: 2,
  restarts: 0,
};
const cronJob: AppWorkload = { ...deployment, kind: "CronJob", name: "nightly" };
const revision = {
  number: 1,
  createdAt: "2026-11-01T10:00:00Z",
  image: "shop/checkout:v1",
  isCurrent: true,
  replicas: 2,
  readyReplicas: 2,
  serving: true,
};

function respond(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

describe("useApplicationDeploys", () => {
  let element: HTMLDivElement;
  let root: Root;
  let client: QueryClient;
  let latest: ReturnType<typeof useApplicationDeploys>;
  const fetchMock = vi.fn<typeof fetch>();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockReset();
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    element = document.createElement("div");
    document.body.append(element);
    root = createRoot(element);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    client.clear();
    element.remove();
  });

  function Probe(props: Parameters<typeof useApplicationDeploys>[0]) {
    latest = useApplicationDeploys(props);
    return null;
  }

  const paint = (node: ReactNode) =>
    act(async () => {
      root.render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
    });

  const flush = () =>
    act(async () => {
      await new Promise((resolve) => setTimeout(resolve));
    });

  const settle = async () => {
    await vi.waitFor(async () => {
      await flush();
      expect(client.isFetching()).toBe(0);
    });
    await flush();
  };

  const history = { data: { appKey: "shop/app/checkout", anchors: [] }, isPending: false, isError: false };
  const built = () => buildAppDeploys(latest!);

  it("reads nothing until the view is on screen", async () => {
    await paint(<Probe workloads={[deployment]} history={history} visible={false} />);

    expect(fetchMock).not.toHaveBeenCalled();
    expect(latest).toBeNull();
  });

  it("reads each workload that has revisions, by its plural kind", async () => {
    fetchMock.mockImplementation(async () => respond(200, [revision]));
    await paint(<Probe workloads={[deployment, cronJob]} history={history} visible />);
    await settle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0][0])).toContain(
      `/workloads/deployments/shop/${deployment.name}/revisions`,
    );
    expect(built().rows).toMatchObject([{ revision: 1, live: true, newest: true, desired: 2 }]);
    expect(built().partial).toBe(false);
  });

  it("reads a null answer as no revisions", async () => {
    fetchMock.mockImplementation(async () => respond(200, null));
    await paint(<Probe workloads={[deployment]} history={history} visible />);
    await settle();

    expect(built()).toMatchObject({ rows: [], partial: false });
  });

  it("keeps the workloads that did refresh when another one fails", async () => {
    const worker = { ...deployment, name: "worker" };
    fetchMock.mockImplementation(async (url) =>
      String(url).includes("/worker/") ? respond(403, { error: "forbidden" }) : respond(200, [revision]),
    );
    await paint(<Probe workloads={[deployment, worker]} history={history} visible />);
    await settle();

    expect(built().rows).toMatchObject([{ live: true, newest: true }]);
    expect(built().partialReasons).toEqual(["Revision history for Deployment worker couldn't be read."]);
  });

  it("shows revisions while the source history loads", async () => {
    fetchMock.mockImplementation(async () => respond(200, [revision]));
    const sourceRef: AppSourceRef = { type: "helm", tool: "helm", kind: "HelmRelease", namespace: "shop", name: "checkout" };
    await paint(<Probe workloads={[deployment]} sourceRef={sourceRef} history={{ isPending: true, isError: false }} visible />);
    await settle();

    expect(built().rows).toMatchObject([{ revision: 1, live: true }]);
    expect(built().stillLoading).toEqual(["Still loading Helm history for checkout."]);
  });

  it("shows the workloads that answered while one is still pending", async () => {
    const worker = { ...deployment, name: "worker" };
    fetchMock.mockImplementation(async (url) =>
      String(url).includes("/worker/") ? new Promise<Response>(() => {}) : respond(200, [revision]),
    );
    await paint(<Probe workloads={[deployment, worker]} history={history} visible />);
    await vi.waitFor(async () => {
      await flush();
      expect(client.isFetching()).toBe(1);
    });
    await flush();

    expect(built().rows).toMatchObject([{ revision: 1, live: true }]);
    expect(built().stillLoading).toEqual(["Still loading revisions for 1 workload."]);
    expect(built().partial).toBe(false);
  });

  describe("request budget", () => {
    const many = Array.from({ length: 100 }, (_, i) => ({ ...deployment, name: `checkout-worker-${i}` }));
    const requests = () => fetchMock.mock.calls.length;
    const advance = (ms: number) =>
      act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
      });

    beforeEach(() => {
      vi.useFakeTimers();
      fetchMock.mockImplementation(async () => respond(200, [revision]));
    });

    afterEach(() => {
      focusManager.setFocused(undefined);
      vi.useRealTimers();
    });

    it("reads a bounded number of workloads per refresh, and says the list is incomplete", async () => {
      await paint(<Probe workloads={many} history={history} visible />);
      await advance(1_000);
      expect(requests()).toBe(MAX_WORKLOADS_READ);

      await advance(20_000);
      expect(requests()).toBe(2 * MAX_WORKLOADS_READ);
      expect(built().partialReasons).toEqual([
        `Showing revisions for ${MAX_WORKLOADS_READ} of 100 workloads. Open a workload to see its own.`,
      ]);
    });

    it("reads the workloads rolling out first", async () => {
      const rollout = { phase: "stalled", active: false, manual: false, label: "Stalled", desired: 1, updated: 0, ready: 0, available: 0 } as const;
      const last = { ...many[99], rollout };
      await paint(<Probe workloads={[...many.slice(0, 99), last]} history={history} visible />);
      await advance(1_000);

      expect(fetchMock.mock.calls.some(([url]) => String(url).includes(last.name))).toBe(true);
    });

    it("stops refreshing while the page is hidden", async () => {
      await paint(<Probe workloads={[deployment]} history={history} visible />);
      await advance(1_000);
      expect(requests()).toBe(1);

      await act(async () => focusManager.setFocused(false));
      await advance(60_000);
      expect(requests()).toBe(1);
    });

    it("stops refreshing when the view closes", async () => {
      await paint(<Probe workloads={[deployment]} history={history} visible />);
      await advance(1_000);

      await paint(<Probe workloads={[deployment]} history={history} visible={false} />);
      await advance(60_000);
      expect(requests()).toBe(1);
    });
  });

  it("says a revision list could not be read rather than showing none", async () => {
    fetchMock.mockImplementation(async () => respond(403, { error: "forbidden" }));
    await paint(<Probe workloads={[deployment]} history={history} visible />);
    await settle();

    expect(built().rows).toEqual([]);
    expect(built().partialReasons).toEqual([
      `Revision history for Deployment ${deployment.name} couldn't be read.`,
    ]);
  });

  it("stops claiming Live and Newest when a refresh fails", async () => {
    fetchMock.mockImplementation(async () => respond(200, [revision]));
    await paint(<Probe workloads={[deployment]} history={history} visible />);
    await settle();
    expect(built().rows[0]).toMatchObject({ live: true, newest: true });

    fetchMock.mockImplementation(async () => respond(500, { error: "unavailable" }));
    await act(async () => {
      await client.refetchQueries({ queryKey: ["workload-revisions"] });
    });
    await settle();

    expect(built().rows[0]).toMatchObject({ live: "unknown", newest: false, stale: true });
    expect(built().partialReasons[0]).toContain(`Couldn't refresh revisions for Deployment ${deployment.name}`);
  });

  it("says a source's history went stale when a refresh fails", async () => {
    fetchMock.mockImplementation(async () => respond(200, [revision]));
    const sourceRef: AppSourceRef = { type: "helm", tool: "helm", kind: "HelmRelease", namespace: "shop", name: "checkout" };
    await paint(
      <Probe workloads={[deployment]} sourceRef={sourceRef} history={{ ...history, isError: true }} visible />,
    );
    await settle();

    expect(built().partialReasons).toEqual(["Couldn't refresh Helm history for checkout. This is the last list read."]);
  });

  it("marks a source's history as missing when it failed to load", async () => {
    fetchMock.mockImplementation(async () => respond(200, [revision]));
    const sourceRef: AppSourceRef = { type: "helm", tool: "helm", kind: "HelmRelease", namespace: "shop", name: "checkout" };
    await paint(
      <Probe workloads={[deployment]} sourceRef={sourceRef} history={{ isPending: false, isError: true }} visible />,
    );
    await settle();

    expect(built().partialReasons).toEqual(["Helm history for checkout isn't available here."]);
  });
});
