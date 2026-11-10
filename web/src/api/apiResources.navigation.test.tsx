// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  customResourceDefinitionRef,
  kindToPluralWithGroup,
  knownKindForPluralWithGroup,
  resetNavigationMap,
} from "@skyhook-io/k8s-ui/utils/navigation";
import { useAPIResources } from "./apiResources";
import { MetadataSection } from "@skyhook-io/k8s-ui/components/ui/drawer-components";

beforeEach(() => vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true));
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  resetNavigationMap();
});

it.each(["unwatched", "syncing", "denied", "deferred", "unsupported", "synced", undefined])(
  "bounds definition polling for inventory state %s",
  async (state) => {
    vi.useFakeTimers();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const root = createRoot(document.createElement("div"));
    const fetch = vi.fn(() => Promise.resolve(Response.json([{
      group: "apiextensions.k8s.io", version: "v1", kind: "CustomResourceDefinition",
      name: "customresourcedefinitions", namespaced: false, isCrd: false, verbs: ["get", "list", "watch"],
      ...(state ? { observation: { state } } : {}),
    }])));
    vi.stubGlobal("fetch", fetch);
    function Probe() {
      useAPIResources();
      return null;
    }
    try {
      await act(async () => root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>));
      await act(async () => { await vi.advanceTimersByTimeAsync(120_001); });
      const calls = fetch.mock.calls.length;
      if (state === "unwatched" || state === "syncing") expect(calls).toBeGreaterThan(1);
      else expect(calls).toBe(1);
      await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
      expect(fetch).toHaveBeenCalledTimes(calls);
    } finally {
      await act(async () => root.unmount());
      client.clear();
    }
  },
);

it("refreshes definitions until their inventory syncs, without relying on topology", async () => {
  vi.useFakeTimers();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const root = createRoot(document.createElement("div"));
  let synced = false;
  const fetch = vi.fn(() => Promise.resolve(Response.json([
    {
      group: "apiextensions.k8s.io", version: "v1", kind: "CustomResourceDefinition",
      name: "customresourcedefinitions", namespaced: false, isCrd: false, verbs: ["get", "list", "watch"],
      observation: { state: synced ? "synced" : "syncing" },
    },
    {
      group: "plants.example.io", version: "v1", kind: "Cactus", name: "cacti",
      namespaced: true, isCrd: true, verbs: ["get"],
      ...(synced ? { definitionName: "cacti.plants.example.io" } : {}),
    },
  ])));
  vi.stubGlobal("fetch", fetch);
  function Probe() {
    useAPIResources();
    return null;
  }
  try {
    await act(async () => {
      root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>);
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(customResourceDefinitionRef("plants.example.io/v1", "Cactus")).toBeNull();
    synced = true;
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(customResourceDefinitionRef("plants.example.io/v1", "Cactus")?.name).toBe("cacti.plants.example.io");
    await act(async () => { await vi.advanceTimersByTimeAsync(6000); });
    expect(fetch).toHaveBeenCalledTimes(2);
  } finally {
    await act(async () => root.unmount());
    client.clear();
  }
});

it("renders a definition link when discovery arrives after the instance", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const element = document.createElement("div");
  const root = createRoot(element);
  const resource = {
    group: "plants.example.io", version: "v1", kind: "Cactus", name: "cacti",
    namespaced: true, isCrd: true, verbs: ["get"], definitionName: "cacti.plants.example.io",
  };
  let finishDiscovery!: (response: Response) => void;
  let fail = false;
  vi.stubGlobal("fetch", vi.fn(() => fail
    ? Promise.resolve(new Response("{}", { status: 500 }))
    : new Promise<Response>((resolve) => { finishDiscovery = resolve; })));
  function Probe() {
    useAPIResources();
    return <MetadataSection data={{ apiVersion: "plants.example.io/v1", kind: "Cactus", metadata: { name: "live" } }} />;
  }
  try {
    await act(async () => root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>));
    expect(element.textContent).not.toContain("cacti.plants.example.io");
    await act(async () => { finishDiscovery(Response.json([resource])); });
    await vi.waitFor(async () => {
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
      expect(element.textContent).toContain("cacti.plants.example.io");
    });
    fail = true;
    await act(async () => { await client.invalidateQueries({ queryKey: ["api-resources"] }); });
    await vi.waitFor(async () => {
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
      expect(element.textContent).not.toContain("cacti.plants.example.io");
    });
  } finally {
    await act(async () => root.unmount());
    client.clear();
  }
});

it("clears confirmed definitions after a failed refresh and cancels discovery from an old cluster", async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const root = createRoot(document.createElement("div"));
  const resource = {
    group: "plants.example.io",
    version: "v1",
    kind: "Cactus",
    name: "cacti",
    namespaced: true,
    isCrd: true,
    verbs: ["get"],
    definitionName: "cacti.plants.example.io",
  };
  let fail = false;
  let hold = false;
  let pendingSignal: AbortSignal | undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn((_input: unknown, init?: RequestInit) => {
      if (hold) {
        pendingSignal = init?.signal as AbortSignal;
        return new Promise<Response>(() => {});
      }
      return Promise.resolve(
        fail ? new Response("{}", { status: 500 }) : Response.json([resource]),
      );
    }),
  );
  function Probe() {
    useAPIResources();
    return null;
  }
  const settle = async () => {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  };
  try {
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <Probe />
        </QueryClientProvider>,
      );
    });
    await vi.waitFor(async () => {
      await settle();
      expect(
        customResourceDefinitionRef("plants.example.io/v1", "Cactus")?.name,
      ).toBe(resource.definitionName);
    });
    expect(
      knownKindForPluralWithGroup("helmreleases", "helm.toolkit.fluxcd.io"),
    ).toBe("HelmRelease");
    expect(
      customResourceDefinitionRef("helm.toolkit.fluxcd.io/v2", "HelmRelease"),
    ).toBeNull();
    fail = true;
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["api-resources"] });
    });
    await vi.waitFor(async () => {
      await settle();
      expect(
        customResourceDefinitionRef("plants.example.io/v1", "Cactus"),
      ).toBeNull();
    });
    expect(
      knownKindForPluralWithGroup("helmreleases", "helm.toolkit.fluxcd.io"),
    ).toBe("HelmRelease");
    expect(kindToPluralWithGroup("Cactus", "plants.example.io")).toBe("cacti");
    fail = false;
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["api-resources"] });
    });
    await vi.waitFor(async () => {
      await settle();
      expect(
        customResourceDefinitionRef("plants.example.io/v1", "Cactus")?.name,
      ).toBe(resource.definitionName);
    });
    hold = true;
    await act(async () => {
      void client.invalidateQueries({ queryKey: ["api-resources"] });
    });
    expect(pendingSignal?.aborted).toBe(false);
    resetNavigationMap();
    await act(async () => {
      client.removeQueries();
    });
    expect(pendingSignal?.aborted).toBe(true);
    expect(
      customResourceDefinitionRef("plants.example.io/v1", "Cactus"),
    ).toBeNull();
  } finally {
    await act(async () => root.unmount());
    client.clear();
  }
});
