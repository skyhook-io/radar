// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { useSetActiveNamespace } from "./client";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

// The server already reports the requested pick, but the POST never answers.
async function timedOutSwitch(cacheScoped: boolean) {
  vi.useFakeTimers();
  const scope = { actives: ["team-a"], kubeconfigNamespace: "", mode: "namespace", accessibleNamespaces: ["team-a"], deniedNamespaces: [], authoritative: true, canClearNamespace: true, cacheScoped };
  const calls: string[] = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? "GET"} ${new URL(url, "http://radar").pathname}`);
    if (init?.method === "POST") {
      return new Promise((_, reject) => init.signal?.addEventListener("abort", () => reject(Object.assign(new Error("aborted"), { name: "AbortError" }))));
    }
    return Promise.resolve(new Response(JSON.stringify(scope), { status: 200, headers: { "Content-Type": "application/json" } }));
  }));
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  client.setQueryData(["namespace-scope"], { ...scope, actives: [] });
  let mutate!: ReturnType<typeof useSetActiveNamespace>["mutateAsync"];
  function Probe() {
    mutate = useSetActiveNamespace().mutateAsync;
    return null;
  }
  const root = createRoot(document.createElement("div"));
  act(() => root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>));
  const outcome = mutate({ namespaces: ["team-a"] }).then(() => "applied", (error: Error) => error.message);
  await vi.advanceTimersByTimeAsync(cacheScoped ? 120_000 : 15_000);
  const result = { outcome: await outcome, calls };
  act(() => root.unmount());
  client.clear();
  return result;
}

it("accepts a timed-out view-filter switch the server applied", async () => {
  const { outcome, calls } = await timedOutSwitch(false);
  expect(outcome).toBe("applied");
  expect(calls).toContain("GET /api/cluster/namespace-scope");
});

it("keeps a timed-out rescope an error even when the scope already names its target", async () => {
  const { outcome, calls } = await timedOutSwitch(true);
  expect(outcome).toMatch(/rescope timed out/);
  expect(calls).not.toContain("GET /api/cluster/namespace-scope");
});
