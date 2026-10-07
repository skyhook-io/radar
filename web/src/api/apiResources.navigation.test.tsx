// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import {
  customResourceDefinitionRef,
  knownKindForPluralWithGroup,
  resetNavigationMap,
} from "@skyhook-io/k8s-ui/utils/navigation";
import { useAPIResources } from "./apiResources";

vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
afterEach(() => {
  vi.unstubAllGlobals();
  resetNavigationMap();
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
      await client.cancelQueries();
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
