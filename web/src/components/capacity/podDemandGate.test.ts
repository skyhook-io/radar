import { describe, expect, it } from "vitest";
import { podAwaitsScheduling, workloadPodAwaitsScheduling } from "./podDemandGate";

describe("podAwaitsScheduling", () => {
  it("admits a Pending pod the scheduler has not placed", () => {
    expect(podAwaitsScheduling({ status: { phase: "Pending" } })).toBe(true);
  });

  it("rejects a Pending pod that already holds a node", () => {
    // ContainerCreating / ImagePullBackOff pods are Pending but absent from
    // Demand — linking them there is a dead end.
    expect(
      podAwaitsScheduling({
        spec: { nodeName: "ip-10-0-1-1" },
        status: { phase: "Pending" },
      }),
    ).toBe(false);
  });

  it("rejects running, succeeded and unknown-shape pods", () => {
    expect(
      podAwaitsScheduling({
        spec: { nodeName: "ip-10-0-1-1" },
        status: { phase: "Running" },
      }),
    ).toBe(false);
    expect(podAwaitsScheduling({ status: { phase: "Succeeded" } })).toBe(false);
    expect(podAwaitsScheduling({})).toBe(false);
    expect(podAwaitsScheduling(undefined)).toBe(false);
  });
});

describe("workloadPodAwaitsScheduling (flat WorkloadPodInfo shape)", () => {
  it("admits an unscheduled Pending pod", () => {
    expect(workloadPodAwaitsScheduling({ phase: "Pending" })).toBe(true);
  });
  it("rejects Pending pods already bound to a node", () => {
    expect(
      workloadPodAwaitsScheduling({ phase: "Pending", nodeName: "ip-10-0-0-1" }),
    ).toBe(false);
  });
  it("rejects running, empty, and absent pods", () => {
    expect(workloadPodAwaitsScheduling({ phase: "Running" })).toBe(false);
    expect(workloadPodAwaitsScheduling({})).toBe(false);
    expect(workloadPodAwaitsScheduling(undefined)).toBe(false);
  });
});
