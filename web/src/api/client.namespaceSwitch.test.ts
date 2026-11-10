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

describe('refreshAfterNamespaceSwitch', () => {
  it('preserves in-flight requests and cached data when switching to All', async () => {
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

    const cancel = vi.spyOn(client, 'cancelQueries')
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    client.setQueryData(['capabilities'], 'unchanged')
    const done = refreshAfterNamespaceSwitch(client, { cacheScoped: false, actives: [] })
    await new Promise((r) => setTimeout(r, 0))
    expect(calls).toBe(1)
    expect(cancel).not.toHaveBeenCalled()
    expect(invalidate).not.toHaveBeenCalled()
    expect(client.getQueryState(['capabilities'])?.isInvalidated).toBe(false)
    resolvers[0]('original request')
    await done
    await vi.waitFor(() => expect(client.getQueryData(['fleet', ''])).toBe('original request'))
    unsubscribe()
  })

  it('leaves queries alone when switching to a named namespace', async () => {
    const client = new QueryClient()
    client.setQueryData(['fleet', 'db'], 'x')
    await refreshAfterNamespaceSwitch(client, { cacheScoped: false, actives: ['db'] })
    expect(client.getQueryState(['fleet', 'db'])?.isInvalidated).toBe(false)
  })
})


it('invalidates inventory after a cache-scoped switch without cancelling requests or the scope query', async () => {
  const client = new QueryClient()
  client.setQueryData(['resource', 'pods', 'db', 'app'], 'pod')
  client.setQueryData(['namespace-scope'], 'scope')
  const cancel = vi.spyOn(client, 'cancelQueries')
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  await refreshAfterNamespaceSwitch(client, { cacheScoped: true, actives: ['db'] })
  expect(cancel).not.toHaveBeenCalled()
  expect(invalidate.mock.calls[0][1]).toEqual({ cancelRefetch: false })
  expect(client.getQueryState(['resource', 'pods', 'db', 'app'])?.isInvalidated).toBe(true)
  expect(client.getQueryState(['namespace-scope'])?.isInvalidated).toBe(false)
})
