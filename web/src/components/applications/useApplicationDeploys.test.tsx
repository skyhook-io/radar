// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { buildAppDeploys, type AppSourceRef, type AppWorkload } from "@skyhook-io/k8s-ui";
import { useApplicationDeploys } from "./useApplicationDeploys";

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
  const built = () => buildAppDeploys(latest.deploys!);

  it("reads nothing until the view is on screen", async () => {
    await paint(<Probe workloads={[deployment]} history={history} visible={false} />);

    expect(fetchMock).not.toHaveBeenCalled();
    expect(latest).toEqual({ deploys: null, deploysPending: true });
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

  it("waits for the source history before answering", async () => {
    fetchMock.mockImplementation(async () => respond(200, [revision]));
    await paint(<Probe workloads={[deployment]} history={{ isPending: true, isError: false }} visible />);
    await settle();

    expect(latest).toEqual({ deploys: null, deploysPending: true });
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
