// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { useNamespaceScope, useSetActiveNamespace } from "./client";
import { readTabPick, writeTabPick } from "../utils/namespacePick";

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

// A tab with the scope query mounted, as App has it, so an error refetches.
function mountTab(client: QueryClient) {
  let mutate!: ReturnType<typeof useSetActiveNamespace>["mutateAsync"];
  function Probe() {
    useNamespaceScope();
    mutate = useSetActiveNamespace().mutateAsync;
    return null;
  }
  const root = createRoot(document.createElement("div"));
  act(() => root.render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>));
  return { mutate: (namespaces: string[]) => mutate({ namespaces }), unmount: () => act(() => root.unmount()) };
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

it("keeps an accepted clear when a newer pick is refused", async () => {
  sessionStorage.clear();
  writeTabPick("/api", "ctx-a", ["team-a"]);
  let server = ["team-a"];
  let releaseClear!: () => void;
  vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => {
    if (init?.method !== "POST") return Promise.resolve(json({ ...tabScope, actives: server }));
    const { namespaces } = JSON.parse(String(init.body)) as { namespaces: string[] };
    if (namespaces.includes("secret-ns")) return Promise.resolve(json({ error: "no access to namespace secret-ns" }, 403));
    return new Promise<Response>(resolve => { releaseClear = () => { server = namespaces; resolve(json({ ...tabScope, actives: server })); }; });
  }));
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const { mutate, unmount } = mountTab(client);
  await vi.waitFor(() => expect(client.getQueryData<{ actives: string[] }>(["namespace-scope"])?.actives).toEqual(["team-a"]));
  const clear = mutate([]);
  await vi.waitFor(() => expect(releaseClear).toBeDefined());
  await mutate(["secret-ns"]).catch(() => undefined);
  releaseClear();
  await clear;
  await vi.waitFor(() => expect(client.isFetching()).toBe(0));
  expect(client.getQueryData<{ actives: string[] }>(["namespace-scope"])?.actives).toEqual([]);
  expect(readTabPick("/api", "ctx-a")).toEqual([]);
  unmount();
});

it("keeps a switch the server applied when its answer is lost", async () => {
  sessionStorage.clear();
  writeTabPick("/api", "ctx-a", ["team-a"]);
  let server = ["team-a"];
  vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => {
    if (init?.method !== "POST") return Promise.resolve(json({ ...tabScope, actives: server }));
    server = (JSON.parse(String(init.body)) as { namespaces: string[] }).namespaces;
    return Promise.reject(new TypeError("Failed to fetch"));
  }));
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const { mutate, unmount } = mountTab(client);
  await vi.waitFor(() => expect(client.getQueryData<{ actives: string[] }>(["namespace-scope"])?.actives).toEqual(["team-a"]));
  await mutate(["team-b"]).catch(() => undefined);
  await vi.waitFor(() => expect(client.isFetching()).toBe(0));
  expect(client.getQueryData<{ actives: string[] }>(["namespace-scope"])?.actives).toEqual(["team-b"]);
  expect(readTabPick("/api", "ctx-a")).toEqual(["team-b"]);
  unmount();
});

it("keeps a switch the server applied when its answer's body is lost", async () => {
  sessionStorage.clear();
  writeTabPick("/api", "ctx-a", ["team-a"]);
  let server = ["team-a"];
  vi.stubGlobal("fetch", vi.fn((_url: string, init?: RequestInit) => {
    if (init?.method !== "POST") return Promise.resolve(json({ ...tabScope, actives: server }));
    server = (JSON.parse(String(init.body)) as { namespaces: string[] }).namespaces;
    const body = new ReadableStream({ start(controller) { controller.error(new TypeError("network error")); } });
    return Promise.resolve(new Response(body, { status: 200, headers: { "Content-Type": "application/json" } }));
  }));
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const { mutate, unmount } = mountTab(client);
  await vi.waitFor(() => expect(client.getQueryData<{ actives: string[] }>(["namespace-scope"])?.actives).toEqual(["team-a"]));
  await mutate(["team-b"]).catch(() => undefined);
  await vi.waitFor(() => expect(client.isFetching()).toBe(0));
  expect(client.getQueryData<{ actives: string[] }>(["namespace-scope"])?.actives).toEqual(["team-b"]);
  expect(readTabPick("/api", "ctx-a")).toEqual(["team-b"]);
  unmount();
});
