import { describe, expect, it } from "vitest";
import {
  describeDrainResult,
  DrainPlanUnsupportedError,
  drainPlanBody,
  drainPlanFetchError,
  drainPlanPath,
} from "./client";

describe("drain plan request", () => {
  it("targets the read-only plan endpoint, never the drain", () => {
    expect(drainPlanPath("worker-1")).toBe("/nodes/worker-1/drain-plan");
    expect(drainPlanPath("worker-1")).not.toContain("/drain\"");
    expect(drainPlanPath("odd node")).toBe("/nodes/odd%20node/drain-plan");
  });

  it("always sends both options explicitly so no server default applies", () => {
    expect(JSON.parse(drainPlanBody({ deleteEmptyDirData: false, force: false }))).toEqual({
      deleteEmptyDirData: false,
      force: false,
    });
    expect(JSON.parse(drainPlanBody({ deleteEmptyDirData: true, force: true }))).toEqual({
      deleteEmptyDirData: true,
      force: true,
    });
  });

  it("reads a plain-text 404 as an unsupported endpoint, not as a missing node", () => {
    expect(drainPlanFetchError(404, null)).toBeInstanceOf(DrainPlanUnsupportedError);
    const nodeGone = drainPlanFetchError(404, { error: 'nodes "worker-1" not found' });
    expect(nodeGone).not.toBeInstanceOf(DrainPlanUnsupportedError);
    expect(nodeGone.message).toBe('nodes "worker-1" not found');
    expect(drainPlanFetchError(503, null).message).toBe("HTTP 503");
  });
});

describe("describeDrainResult", () => {
  it("lists evictions, skips with reasons, and individual failures", () => {
    const r = describeDrainResult({
      evictedPods: ["shop/cache"],
      skippedPods: [
        { namespace: "shop", name: "agent", outcome: "skip", reason: "managed by a DaemonSet", emptyDir: false, pdbChecked: false },
      ],
      errors: ["shop/web-1: timed out waiting for PDB to allow eviction"],
    });
    expect(r.failed).toBe(true);
    expect(r.title).toBe("Drain finished with 1 failed eviction(s): 1 evicted, 1 skipped");
    expect(r.detail).toContain("shop/agent (managed by a DaemonSet)");
    expect(r.detail).toContain("shop/web-1: timed out");
  });

  it("caps long lists so the toast stays readable", () => {
    const skippedPods = Array.from({ length: 8 }, (_, i) => ({
      namespace: "ns", name: `p${i}`, outcome: "skip" as const, reason: "r", emptyDir: false, pdbChecked: false,
    }));
    const r = describeDrainResult({ evictedPods: [], skippedPods });
    expect(r.detail).toContain("Skipped 8:");
    expect(r.detail).toContain("ns/p4 (r); +3 more");
    expect(r.detail).not.toContain("ns/p5");
  });

  it("reports a clean drain as success and says what success means", () => {
    const r = describeDrainResult({ evictedPods: ["a", "b"] });
    expect(r.failed).toBe(false);
    expect(r.title).toBe("Node drained: 2 evicted, 0 skipped");
    expect(r.detail).toContain("those pods may still be terminating");
    expect(r.detail).toContain("node remains cordoned");
  });

  it("never claims evictions were accepted when nothing was evicted", () => {
    const allFailed = describeDrainResult({
      evictedPods: [],
      errors: ["shop/web-1: timed out waiting for PDB to allow eviction"],
    });
    expect(allFailed.detail).not.toContain("accepted");
    expect(allFailed.detail).toContain("node remains cordoned");

    const skipOnly = describeDrainResult({
      evictedPods: [],
      skippedPods: [
        { namespace: "shop", name: "agent", outcome: "skip", reason: "managed by a DaemonSet", emptyDir: false, pdbChecked: false },
      ],
    });
    expect(skipOnly.detail).not.toContain("accepted");
  });
});
