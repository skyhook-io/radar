import { describe, expect, it } from "vitest";
import type { CapacityDemandGroup } from "@skyhook-io/k8s-ui";
import { demandGroupVerdict } from "./CapacityDemand";
import { quantityText } from "./shared";

function group(counts: { declaredCompatible: number; incompatible: number; unknown: number }): CapacityDemandGroup {
  const incompatible = Array.from({ length: counts.incompatible }, (_, i) => ({
    result: "incompatible",
    evidence: [{ predicate: "requirements", explanation: `pool ${i} requires arm64` }],
  }));
  return { poolEvaluationCounts: counts, poolEvaluations: incompatible } as unknown as CapacityDemandGroup;
}

describe("demandGroupVerdict", () => {
  it("rules a group out when every evaluated pool is incompatible", () => {
    expect(demandGroupVerdict(group({ declaredCompatible: 0, incompatible: 2, unknown: 0 }))?.poolCount).toBe(2);
  });

  it("gives no verdict while any pool is unknown", () => {
    expect(demandGroupVerdict(group({ declaredCompatible: 0, incompatible: 5, unknown: 1 }))).toBeNull();
  });
});

describe("quantityText", () => {
  it("names GPU, pod and extended quantities", () => {
    const observation = {
      certainty: "exact",
      resources: { cpu: "100m", memory: "32Mi", "nvidia.com/gpu": "2", pods: "1" },
    };
    expect(quantityText(observation as never)).toBe("0.10 cores · 32 MiB · gpu 2 · 1 pod");
    expect(quantityText({ certainty: "exact", resources: { pods: "3", "example.com/fpga": "1" } } as never)).toBe(
      "example.com/fpga 1 · 3 pods",
    );
    expect(quantityText({ certainty: "exact", resources: { "gpu.intel.com/millicores": "500" } } as never)).toBe(
      "gpu.intel.com/millicores 500",
    );
  });
});
