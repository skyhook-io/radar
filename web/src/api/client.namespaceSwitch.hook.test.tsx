// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { useSetActiveNamespace } from "./client";
import { readTabPick } from "../utils/namespacePick";

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

const tabScope = { actives: [] as string[], context: "ctx-a", kubeconfigNamespace: "", mode: "cluster-wide", accessibleNamespaces: ["team-a", "team-b"], deniedNamespaces: [], authoritative: true, canClearNamespace: true, cacheScoped: false, namespaceRescope: false };

// Each POST answers when its gate opens, with the server's stored pick after
// the write, which another tab may already have changed.
function serveSwitches(serverActives: () => string[]) {
  const gates: Array<() => void> = [];
  vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => {
    const body = () => new Response(JSON.stringify({ ...tabScope, actives: serverActives() }), { status: 200, headers: { "Content-Type": "application/json" } });
    if (init?.method !== "POST") return Promise.resolve(body());
    return new Promise<Response>(resolve => gates.push(() => resolve(body())));
  }));
  return gates;
}

function mountSwitcher(client: QueryClient) {
  let mutate!: ReturnType<typeof useSetActiveNamespace>["mutateAsync"];
  function Probe() {
    mutate = useSetActiveNamespace().mutateAsync;
    return null;
  }
  const root = createRoot(document.createElement("div"));
  act(() => root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>));
  return { mutate: (namespaces: string[]) => mutate({ namespaces }), unmount: () => act(() => root.unmount()) };
}

it("records the pick this tab asked for, not the one another tab stored meanwhile", async () => {
  sessionStorage.clear();
  const gates = serveSwitches(() => ["team-b"]);
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  client.setQueryData(["namespace-scope"], tabScope);
  const { mutate, unmount } = mountSwitcher(client);
  const done = mutate(["team-a", "team-a", ""]);
  await vi.waitFor(() => expect(gates).toHaveLength(1));
  gates[0]();
  await done;
  expect(client.getQueryData<{ actives: string[] }>(["namespace-scope"])?.actives).toEqual(["team-a"]);
  expect(readTabPick("/api", "ctx-a")).toEqual(["team-a"]);
  unmount();
});

it("ignores a switch that answers after a newer one", async () => {
  sessionStorage.clear();
  const gates = serveSwitches(() => []);
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  client.setQueryData(["namespace-scope"], tabScope);
  const { mutate, unmount } = mountSwitcher(client);
  const older = mutate(["team-b"]);
  const newer = mutate([]);
  await vi.waitFor(() => expect(gates).toHaveLength(2));
  gates[1]();
  await newer;
  gates[0]();
  await older;
  expect(client.getQueryData<{ actives: string[] }>(["namespace-scope"])?.actives).toEqual([]);
  expect(readTabPick("/api", "ctx-a")).toEqual([]);
  unmount();
});

it("doesn't record a pick whose answer lands after a context switch", async () => {
  sessionStorage.clear();
  const gates = serveSwitches(() => []);
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  client.setQueryData(["namespace-scope"], tabScope);
  const { mutate, unmount } = mountSwitcher(client);
  const done = mutate(["team-a"]);
  await vi.waitFor(() => expect(gates).toHaveLength(1));
  client.setQueryData(["namespace-scope"], { ...tabScope, context: "ctx-b" });
  gates[0]();
  await done;
  expect(client.getQueryData<{ context: string }>(["namespace-scope"])?.context).toBe("ctx-b");
  expect(readTabPick("/api", "ctx-a")).toBeUndefined();
  unmount();
});

it("drops a requested namespace the answer shows is gone", async () => {
  sessionStorage.clear();
  const gates = serveSwitches(() => ["team-a"]);
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  client.setQueryData(["namespace-scope"], tabScope);
  const { mutate, unmount } = mountSwitcher(client);
  const done = mutate(["team-a", "deleted-meanwhile"]);
  await vi.waitFor(() => expect(gates).toHaveLength(1));
  gates[0]();
  await done;
  expect(client.getQueryData<{ actives: string[] }>(["namespace-scope"])?.actives).toEqual(["team-a"]);
  expect(readTabPick("/api", "ctx-a")).toEqual(["team-a"]);
  unmount();
});
