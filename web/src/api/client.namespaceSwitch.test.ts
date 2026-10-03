import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { reconcileNamespaceSwitch, refreshAfterNamespaceSwitch } from "./client";

function serveScope(actives: string[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      new Response(
        JSON.stringify({
          actives,
          kubeconfigNamespace: "default",
          mode: actives.length ? "namespace" : "cluster-wide",
          accessibleNamespaces: [],
          deniedNamespaces: [],
          authoritative: true,
          canClearNamespace: true,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    ),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("reconcileNamespaceSwitch", () => {
  it("returns the server's scope when a timed-out switch was applied", async () => {
    serveScope([]);
    expect((await reconcileNamespaceSwitch([]))?.actives).toEqual([]);
    serveScope(["b", "a"]);
    expect((await reconcileNamespaceSwitch(["a", "b"]))?.actives).toEqual([
      "b",
      "a",
    ]);
  });

  it("reports nothing when the server still holds another pick", async () => {
    serveScope(["default"]);
    expect(await reconcileNamespaceSwitch([])).toBeNull();
  });

  it("reports nothing when the scope cannot be read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("network down");
      }),
    );
    expect(await reconcileNamespaceSwitch([])).toBeNull();
  });
});

// A first fetch for "All namespaces" can leave before the switch lands and
// read the old pick on the server; the refresh must send a new request.
describe('refreshAfterNamespaceSwitch', () => {
  it('replaces a first fetch still in flight when switching to All', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    let calls = 0
    const resolvers: ((v: string) => void)[] = []
    const observer = new QueryObserver(client, {
      queryKey: ['fleet', ''],
      queryFn: () => {
        calls++
        return new Promise<string>((resolve) => resolvers.push(resolve))
      },
    })
    const unsubscribe = observer.subscribe(() => {})
    await Promise.resolve()
    expect(calls).toBe(1)

    const done = refreshAfterNamespaceSwitch(client, { cacheScoped: false, actives: [] })
    await new Promise((r) => setTimeout(r, 0))
    expect(calls).toBe(2)
    resolvers[0]('old pick')
    resolvers[1]('all namespaces')
    await done
    expect(client.getQueryData(['fleet', ''])).toBe('all namespaces')
    unsubscribe()
  })

  it('leaves queries alone when switching to a named namespace', async () => {
    const client = new QueryClient()
    client.setQueryData(['fleet', 'db'], 'x')
    await refreshAfterNamespaceSwitch(client, { cacheScoped: false, actives: ['db'] })
    expect(client.getQueryState(['fleet', 'db'])?.isInvalidated).toBe(false)
  })
})
