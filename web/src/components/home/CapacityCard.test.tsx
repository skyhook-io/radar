import { describe, expect, it, vi } from "vitest";
import { renderToString } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type {
  CapacityOverviewResponse,
  CapacityResponseMeta,
  CapacitySourceCoverage,
} from "@skyhook-io/k8s-ui";
import { CapacityCard } from "./CapacityCard";

vi.mock("../../contexts/CapabilitiesContext", () => ({
  useCapabilitiesContext: () => ({ karpenter: { state: "available" } }),
}));

const generatedAt = "2026-07-13T08:00:00Z";

function sourceCoverage(
  scope: CapacitySourceCoverage["scope"] = "cluster",
): CapacitySourceCoverage {
  return {
    status: "available",
    scope,
    observedAt: generatedAt,
    impactFields: [],
  };
}

const meta: CapacityResponseMeta = {
  schemaVersion: "v1alpha1",
  generatedAt,
  clusterContext: { contextName: "radar-test-eks" },
  provider: {
    type: "karpenter",
    controllerMode: "self_managed",
    apiVersionsByKind: {},
    nodeClassKinds: [],
    features: {},
  },
  coverage: { nodes: sourceCoverage(), pods: sourceCoverage() },
};

function overview(
  summary: Partial<CapacityOverviewResponse["summary"]> = {},
  coverage: CapacityResponseMeta["coverage"] = meta.coverage,
): CapacityOverviewResponse {
  return {
    ...meta,
    coverage,
    state: "available",
    summary: {
      actions: [],
      poolCount: 2,
      claimCount: 4,
      nodeCount: 6,
      pendingPodCount: 3,
      managers: [],
      ...summary,
    },
    pools: [],
    poolsTruncated: false,
    groups: [],
    orphanAutoscalerGroups: [],
    orphanAutoscalerGroupsMeta: { total: 0, returned: 0, truncated: false },
  };
}

function renderCard(data: CapacityOverviewResponse): string {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, retryOnMount: false } },
  });
  client.setQueryData(["capacity", "overview"], data);
  return renderToString(
    <QueryClientProvider client={client}>
      <CapacityCard onNavigate={() => {}} />
    </QueryClientProvider>,
  );
}

describe("CapacityCard", () => {
  it("shows exact counts under cluster-wide coverage", () => {
    const html = renderCard(overview());
    expect(html).toContain(">3<");
    expect(html).not.toContain("≥");
  });

  it("hedges the pending-pod count when pod coverage is a lower bound", () => {
    const html = renderCard(
      overview(
        {},
        {
          ...meta.coverage,
          pods: sourceCoverage("all_authorized_namespaces"),
        },
      ),
    );
    expect(html).toContain("≥3");
  });

  it("renders an omitted NodePool count as unavailable, never zero", () => {
    // The server omits poolCount when NodePools were not observed.
    const html = renderCard(overview({ poolCount: undefined }));
    expect(html).toContain("NodePools");
    expect(html).toContain("—");
    expect(html).not.toContain(">0<");
  });
});
