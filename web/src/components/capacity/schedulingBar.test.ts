import { describe, expect, it } from "vitest";
import type { CapacityQuantityObservation } from "@skyhook-io/k8s-ui";
import {
  deriveSchedulingRows,
  quantityToNumber,
} from "./ClusterSchedulingCard";
import { claimStagesDetail } from "./CapacityOverview";

function observation(
  resources: Record<string, string>,
  certainty: CapacityQuantityObservation["certainty"] = "exact",
): CapacityQuantityObservation {
  return {
    resources,
    certainty,
    sources: ["test"],
    asOf: "2026-07-16T00:00:00Z",
    granularity: "aggregate",
  };
}

describe("deriveSchedulingRows", () => {
  it("builds proportional rows and keeps pod slots out", () => {
    const rows = deriveSchedulingRows(
      {
        scheduledRequests: observation({
          cpu: "21900m",
          memory: "79Gi",
          pods: "27",
        }),
        allocatable: observation({
          cpu: "31200m",
          memory: "102Gi",
          pods: "220",
        }),
        inFlightCapacity: observation({ cpu: "4" }),
      },
      observation({ cpu: "48750m", memory: "193Gi", "nvidia.com/gpu": "8" }),
    );
    expect(rows.map((row) => row.resource)).toEqual([
      "cpu",
      "memory",
      "nvidia.com/gpu",
    ]);

    const cpu = rows[0];
    expect(cpu.requested).toBe("21900m");
    expect(cpu.allocatable).toBe("31200m");
    expect(cpu.inFlight).toBe("4");
    expect(cpu.pending).toBe("48750m");
    expect(cpu.requestedFraction).toBeCloseTo(21.9 / 31.2, 5);
    expect(cpu.inFlightFraction).toBeCloseTo(4 / 31.2, 5);
    expect(cpu.certainty).toBe("exact");

    // GPU: present allocatable observation without the key is a structural
    // zero, and the row exists because pending demand names the resource.
    const gpu = rows[2];
    expect(gpu.allocatable).toBe("0");
    expect(gpu.requested).toBe("0");
    expect(gpu.pending).toBe("8");
    expect(gpu.requestedFraction).toBeNull();
    expect(gpu.inFlight).toBeNull();
  });

  it("degrades row certainty to the worst present component", () => {
    const rows = deriveSchedulingRows(
      {
        scheduledRequests: observation({ cpu: "1" }),
        allocatable: observation({ cpu: "4" }, "lower_bound"),
      },
      observation({ cpu: "0" }),
    );
    expect(rows[0].certainty).toBe("lower_bound");
  });

  it("treats an unobserved source as unknown, never zero", () => {
    const rows = deriveSchedulingRows(
      { scheduledRequests: observation({ cpu: "1" }) },
      undefined,
    );
    const cpu = rows.find((row) => row.resource === "cpu");
    expect(cpu?.allocatable).toBeNull();
    expect(cpu?.pending).toBeNull();
    expect(cpu?.certainty).toBe("unknown");
    expect(cpu?.requestedFraction).toBeNull();
  });

  it("never lets absent in-flight degrade the row", () => {
    const rows = deriveSchedulingRows(
      {
        scheduledRequests: observation({ cpu: "1" }),
        allocatable: observation({ cpu: "4" }),
      },
      observation({}),
    );
    expect(rows[0].inFlight).toBeNull();
    expect(rows[0].certainty).toBe("exact");
  });

  it("drops rows carrying no signal at all", () => {
    const rows = deriveSchedulingRows(
      {
        scheduledRequests: observation({ cpu: "1", "example.com/fpga": "0" }),
        allocatable: observation({ cpu: "4" }),
      },
      observation({}),
    );
    // Memory and the fpga row are all-zero against observed sources — noise.
    expect(rows.map((row) => row.resource)).toEqual(["cpu"]);
  });

  it("returns no rows for an empty fleet with no demand", () => {
    // One pool, zero nodes, zero claims, nothing pending: every value is an
    // observed zero. Stub bars would read as broken UI — the card renders an
    // honest empty state instead.
    const rows = deriveSchedulingRows(
      {
        scheduledRequests: observation({}),
        allocatable: observation({}),
      },
      observation({}),
    );
    expect(rows).toEqual([]);
  });
});

describe("deriveSchedulingRows negative-priority split", () => {
  it("computes a proportional fraction under exact coverage", () => {
    const rows = deriveSchedulingRows(
      {
        scheduledRequests: observation({ cpu: "6", memory: "24Gi" }),
        allocatable: observation({ cpu: "12", memory: "48Gi" }),
        negativePriorityRequests: observation({ cpu: "3" }),
      },
      undefined,
    );
    const cpu = rows.find((row) => row.resource === "cpu");
    expect(cpu?.negativePriority).toBe("3");
    expect(cpu?.negativePriorityCertainty).toBe("exact");
    expect(cpu?.negativePriorityFraction).toBeCloseTo(3 / 12, 5);
    // Memory carries no negative-priority key: a present observation missing the
    // resource is a structural "0", never a band.
    const memory = rows.find((row) => row.resource === "memory");
    expect(memory?.negativePriority).toBe("0");
    expect(memory?.negativePriorityFraction).toBeNull();
  });

  it("draws no proportional split under a lower-bound observation", () => {
    const rows = deriveSchedulingRows(
      {
        scheduledRequests: observation({ cpu: "6" }, "lower_bound"),
        allocatable: observation({ cpu: "12" }),
        negativePriorityRequests: observation({ cpu: "3" }, "lower_bound"),
      },
      undefined,
    );
    const cpu = rows.find((row) => row.resource === "cpu");
    // Two lower bounds cannot establish a share — the value survives for the
    // text annotation, but no proportional band is drawn.
    expect(cpu?.negativePriority).toBe("3");
    expect(cpu?.negativePriorityCertainty).toBe("lower_bound");
    expect(cpu?.negativePriorityFraction).toBeNull();
  });

  it("requires allocatable itself to be exact for a proportional split", () => {
    const rows = deriveSchedulingRows(
      {
        scheduledRequests: observation({ cpu: "6" }),
        allocatable: observation({ cpu: "12" }, "lower_bound"),
        negativePriorityRequests: observation({ cpu: "3" }),
      },
      undefined,
    );
    const cpu = rows.find((row) => row.resource === "cpu");
    expect(cpu?.negativePriority).toBe("3");
    expect(cpu?.negativePriorityFraction).toBeNull();
  });

  it("leaves negative-priority null when the field is absent", () => {
    const rows = deriveSchedulingRows(
      {
        scheduledRequests: observation({ cpu: "6" }),
        allocatable: observation({ cpu: "12" }),
      },
      undefined,
    );
    const cpu = rows.find((row) => row.resource === "cpu");
    expect(cpu?.negativePriority).toBeNull();
    expect(cpu?.negativePriorityCertainty).toBeNull();
    expect(cpu?.negativePriorityFraction).toBeNull();
  });
});

describe("quantityToNumber", () => {
  it("parses cpu, memory, and extended counts", () => {
    expect(quantityToNumber("cpu", "500m")).toBeGreaterThan(0);
    expect(quantityToNumber("cpu", "0")).toBe(0);
    expect(quantityToNumber("memory", "1Gi")).toBe(1024 ** 3);
    expect(quantityToNumber("nvidia.com/gpu", "8")).toBe(8);
    expect(quantityToNumber("nvidia.com/gpu", "not-a-number")).toBeNull();
    // Milli quantities must not read 1000× large through the byte parser.
    expect(quantityToNumber("example.com/widget", "3000m")).toBe(3);
    expect(quantityToNumber("cpu", "1500m")).toBe(1.5e9);
  });
});

describe("claimStagesDetail", () => {
  it("lists nonzero stages in lifecycle order with the orphan note", () => {
    expect(
      claimStagesDetail(
        {
          total: 10,
          pending: 0,
          launched: 1,
          registered: 0,
          initialized: 0,
          ready: 8,
          failed: 1,
          terminating: 0,
        },
        1,
      ),
    ).toBe("8 ready · 1 launched · 1 failed · +1 orphaned");
  });

  it("is honest about an empty fleet and absent rollups", () => {
    expect(
      claimStagesDetail(
        {
          total: 0,
          pending: 0,
          launched: 0,
          registered: 0,
          initialized: 0,
          ready: 0,
          failed: 0,
          terminating: 0,
        },
        0,
      ),
    ).toBe("none yet");
    expect(claimStagesDetail(undefined, 3)).toBeNull();
  });
});
