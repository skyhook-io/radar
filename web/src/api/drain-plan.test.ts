import { describe, expect, it } from "vitest";
import {
  describeDrainResult,
  drainPlanBody,
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
});

describe("describeDrainResult", () => {
  const pod = (name: string, expectedSkip = false): import("@skyhook-io/k8s-ui").DrainPlanPod => ({
    namespace: "shop", name, outcome: "skip", reason: expectedSkip ? "expected exclusion" : "protected workload",
    emptyDir: false, pdbChecked: false, expectedSkip,
  });

  it("confirms required removals without promising shutdown safety", () => {
    const result = describeDrainResult({ evictedPods: ["shop/web"], waitedForDeletion: true, outcome: "complete", skippedPods: [pod("system", true)] });
    expect(result).toMatchObject({ title: "Drain complete: 1 pod removed", type: "success", outcome: "complete", failed: false });
    expect(result.detail).toContain("All evicted pods have left the node");
    expect(result.detail).toContain("Left in place by design: 1 pod");
    expect(result.detail).not.toMatch(/empty|safe to/i);
  });

  it("gives a skipped running workload an incomplete warning, never green success", () => {
    const result = describeDrainResult({ evictedPods: ["shop/web"], waitedForDeletion: true, skippedPods: [pod("bare")] });
    expect(result).toMatchObject({ title: "Drain incomplete: 1 pod left on the node", type: "warning", outcome: "incomplete", failed: true });
    expect(result.detail).toContain("shop/bare (protected workload)");
    expect(result.detail).toContain("Do not take the node down");
  });

  it("does not turn terminal or system exclusions into failure", () => {
    const result = describeDrainResult({ skippedPods: [pod("completed", true), pod("static", true), pod("daemon", true)] });
    expect(result).toMatchObject({ title: "Nothing to evict", type: "success", outcome: "complete", failed: false });
    expect(result.detail).toContain("Left in place by design: 3 pods");
    expect(result.detail).not.toContain("Do not take the node down");
  });

  it("shows observed remaining pods as incomplete", () => {
    const result = describeDrainResult({ evictedPods: ["shop/web"], waitedForDeletion: true, pendingPods: ["shop/web"] });
    expect(result).toMatchObject({ title: "Drain incomplete: 1 pod left on the node", type: "warning", outcome: "incomplete" });
    expect(result.detail).toContain("Still on the node 1: shop/web");
    expect(result.detail).not.toContain("All evicted pods have left");
  });

  it("preserves the read error without turning unknown into confirmed presence", () => {
    const result = describeDrainResult({ evictedPods: ["shop/web"], waitedForDeletion: true, unconfirmedPods: ["shop/web"], verificationError: "pod listing unavailable", outcome: "unconfirmed" });
    expect(result).toMatchObject({ title: "Could not confirm drain completion", type: "warning", outcome: "unconfirmed", failed: true });
    expect(result.detail).toContain("Deletion unconfirmed 1: shop/web");
    expect(result.detail).toContain("pod listing unavailable");
    expect(result.detail).not.toMatch(/still on the node|All evicted pods have left/);
  });

  it("reports failed evictions and unknown siblings together", () => {
    const result = describeDrainResult({ errors: ["shop/db: refused"], unconfirmedPods: ["shop/web"], verificationError: "list failed", outcome: "incomplete" });
    expect(result).toMatchObject({ title: "Drain incomplete: 1 failed eviction", type: "error", outcome: "incomplete" });
    expect(result.detail).toContain("Failed evictions 1: shop/db: refused");
    expect(result.detail).toContain("Deletion unconfirmed 1: shop/web");
    expect(result.detail).not.toContain("Still on the node");
  });

  it("leads an explicit no-wait result with started and informational treatment", () => {
    const result = describeDrainResult({ evictedPods: ["shop/web"], waitedForDeletion: false, outcome: "started" });
    expect(result).toMatchObject({ title: "Evictions started: 1 accepted", type: "info", outcome: "started", failed: false });
    expect(result.detail).toContain("pod deletion has not been confirmed");
  });

  it("keeps older backend acceptance and unknown skip metadata conservative", () => {
    expect(describeDrainResult({ evictedPods: ["shop/web"] })).toMatchObject({ outcome: "started", type: "info" });
    expect(describeDrainResult({ skippedPods: [{ namespace: "system", name: "old-agent", outcome: "skip", reason: "managed by a DaemonSet", emptyDir: false, pdbChecked: false }] })).toMatchObject({ outcome: "unconfirmed", type: "warning" });
  });

  it("does not classify legacy skip reasons as known blockers", () => {
    const legacy = { namespace: "system", name: "agent", outcome: "skip" as const, reason: "managed by a DaemonSet", emptyDir: false, pdbChecked: false };
    const result = describeDrainResult({ evictedPods: ["shop/web"], skippedPods: [legacy] });
    expect(result).toMatchObject({ outcome: "started", type: "info" });
    expect(result.detail).toContain("Skipped pods requiring review 1");
    expect(result.detail).not.toContain("requiring attention");
  });

  it("uses the backend completion classification when present", () => {
    expect(describeDrainResult({ outcome: "complete", skippedPods: [pod("excluded")] })).toMatchObject({ outcome: "complete", type: "success" });
  });

  it("caps long pod lists", () => {
    const result = describeDrainResult({ skippedPods: Array.from({ length: 8 }, (_, i) => pod(`p${i}`)) });
    expect(result.detail).toContain("shop/p4 (protected workload); +3 more");
    expect(result.detail).not.toContain("shop/p5");
  });
});
