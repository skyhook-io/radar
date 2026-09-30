import { afterEach, describe, expect, it, vi } from "vitest";
import { reconcileNamespaceSwitch } from "./client";

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
