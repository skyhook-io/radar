import { describe, expect, it } from "vitest";
import { ApiError, goneRecheckInterval, refetchOnResourceEvents, resourceEventKey } from "./client";

const notFound = new ApiError('pods "web-1" not found', 404);
const detail = (namespace: string, name: string, error: unknown) => ({
  queryKey: ["resource", "pods", namespace, name, undefined],
  state: { error },
});

describe("refetchOnResourceEvents", () => {
  it("refetches a live detail view on any change to its kind", () => {
    expect(refetchOnResourceEvents(detail("shop", "web-1", null), "pods", new Set())).toBe(true);
  });

  it("refetches a detail view that failed for a reason other than 404", () => {
    const failed = detail("shop", "web-1", new ApiError("unavailable", 503));
    expect(refetchOnResourceEvents(failed, "pods", new Set())).toBe(true);
  });

  it("leaves a deleted object alone when other objects of its kind change", () => {
    // The production shape: pods churning next to an open view of a deleted
    // pod refetched it, 404 every time, for as long as the view stayed open.
    const named = new Set([resourceEventKey("pods", "shop", "web-2")]);
    expect(refetchOnResourceEvents(detail("shop", "web-1", notFound), "pods", named)).toBe(false);
  });

  it("refetches a deleted object as soon as an event names it", () => {
    // A StatefulSet recreates its pod under the same name.
    const named = new Set([resourceEventKey("pods", "shop", "web-1")]);
    expect(refetchOnResourceEvents(detail("shop", "web-1", notFound), "pods", named)).toBe(true);
  });

  it("matches a cluster-scoped object, whose namespace is empty", () => {
    const query = { queryKey: ["resource", "nodes", "", "node-a", undefined], state: { error: notFound } };
    expect(refetchOnResourceEvents(query, "nodes", new Set([resourceEventKey("nodes", "", "node-a")]))).toBe(true);
  });
});

describe("goneRecheckInterval", () => {
  it("re-checks a deleted object once a minute", () => {
    expect(goneRecheckInterval({ state: { error: notFound } }, undefined)).toBe(60_000);
    expect(goneRecheckInterval({ state: { error: notFound } }, 5_000)).toBe(60_000);
  });

  it("keeps the caller's interval otherwise", () => {
    expect(goneRecheckInterval({ state: { error: null } }, 5_000)).toBe(5_000);
    expect(goneRecheckInterval({ state: { error: new ApiError("unavailable", 503) } }, undefined)).toBeUndefined();
  });
});
