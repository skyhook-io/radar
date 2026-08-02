import { describe, expect, it, vi } from "vitest";
import { renderToString } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type {
  CapacityBoundedResultMeta,
  CapacityDemandGroup,
  CapacityDemandResponse,
  CapacityDemandSummary,
  CapacityQuantityObservation,
  CapacityResponseMeta,
  CapacitySourceCoverage,
} from "@skyhook-io/k8s-ui";
import { CapacityView } from "./CapacityView";

vi.mock("../../context/ConnectionContext", () => ({
  useConnection: () => ({ connection: { state: "connected" } }),
}));

const generatedAt = "2026-07-13T08:00:00Z";

function sourceCoverage(
  status: CapacitySourceCoverage["status"] = "available",
): CapacitySourceCoverage {
  return {
    status,
    scope: "cluster",
    observedAt: generatedAt,
    impactFields: [],
  };
}

const meta: CapacityResponseMeta = {
  schemaVersion: "v1alpha1",
  generatedAt,
  clusterContext: { contextName: "radar-test-eks", clusterName: "radar-test" },
  provider: {
    type: "karpenter",
    controllerMode: "self_managed",
    apiVersionsByKind: { NodePool: ["v1"], NodeClaim: ["v1"] },
    nodeClassKinds: [],
    features: { staticCapacity: true, metrics: true },
  },
  coverage: {
    nodePools: sourceCoverage(),
    nodeClaims: sourceCoverage(),
    nodeClasses: sourceCoverage(),
    nodes: sourceCoverage(),
    pods: sourceCoverage(),
    workloads: sourceCoverage(),
    nodeMetrics: sourceCoverage(),
    karpenterObjectEvents: sourceCoverage(),
    timeline: sourceCoverage(),
  },
};

function quantity(
  resources: Record<string, string>,
): CapacityQuantityObservation {
  return {
    resources,
    certainty: "exact",
    sources: ["karpenter.status"],
    asOf: generatedAt,
    granularity: "aggregate",
  };
}

function boundedMeta(count: number): CapacityBoundedResultMeta {
  return { total: count, returned: count, truncated: false };
}

const demandGroup: CapacityDemandGroup = {
  id: "grp-1",
  fingerprint: "sched-fp:abc123",
  owner: { kind: "Deployment", namespace: "payments", name: "checkout" },
  pods: [{ ref: { kind: "Pod", namespace: "payments", name: "checkout-abc" } }],
  podsMeta: boundedMeta(1),
  namespace: "payments",
  firstSeen: generatedAt,
  lastSeen: generatedAt,
  podCount: 3,
  perPodRequests: quantity({ cpu: "2", memory: "4Gi" }),
  aggregateRequests: quantity({ cpu: "6", memory: "12Gi" }),
  schedulingSignature: {
    fingerprint: "sched-fp:abc123",
    constraints: [],
    constraintsMeta: boundedMeta(0),
    tolerations: [],
    tolerationsMeta: boundedMeta(0),
  },
  state: "awaiting_capacity",
  schedulerReasons: [],
  schedulerReasonsMeta: boundedMeta(0),
  poolEvaluations: [],
  poolEvaluationsMeta: boundedMeta(0),
  poolEvaluationCounts: { declaredCompatible: 0, incompatible: 0, unknown: 0 },
  issues: [],
};

const demandSummary: CapacityDemandSummary = {
  total: { podCount: 3, groupCount: 1 },
  byState: {
    waiting_for_scheduler: { podCount: 0, groupCount: 0 },
    held: { podCount: 0, groupCount: 0 },
    awaiting_capacity: { podCount: 3, groupCount: 1 },
    blocked: { podCount: 0, groupCount: 0 },
    unknown: { podCount: 0, groupCount: 0 },
  },
};

function demandResponse(
  group: CapacityDemandGroup = demandGroup,
): CapacityDemandResponse {
  return {
    ...meta,
    state: "available",
    summary: demandSummary,
    items: [group],
    page: { hasMore: false },
  };
}

function renderDemand(group: CapacityDemandGroup): string {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, retryOnMount: false } },
  });
  client.setQueryData(
    ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
    demandResponse(group),
  );
  return renderToString(
    <MemoryRouter initialEntries={["/capacity/demand"]}>
      <QueryClientProvider client={client}>
        <CapacityView onOpenResource={() => {}} />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("CapacityDemand nomination chip", () => {
  it("renders a nomination chip when the group holds nominated pods", () => {
    const html = renderDemand({ ...demandGroup, nominatedPodCount: 2 });
    expect(html).toContain("2 nominated");
  });

  it("omits the nomination chip when no pods are nominated", () => {
    const html = renderDemand({ ...demandGroup, nominatedPodCount: undefined });
    expect(html).not.toContain("nominated");
  });
});
