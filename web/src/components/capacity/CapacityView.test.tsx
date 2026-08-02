import { describe, expect, it, vi } from "vitest";
import { renderToString } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type {
  CapacityActivityEpisode,
  CapacityActivityResponse,
  CapacityAutoscalerChildObservation,
  CapacityBoundedResultMeta,
  CapacityDemandGroup,
  CapacityDemandResponse,
  CapacityDemandSummary,
  CapacityGroupSummary,
  CapacityManagerSummary,
  CapacityMemberListResponse,
  CapacityOverviewResponse,
  CapacityPoolDetailResponse,
  CapacityPoolListResponse,
  CapacityPoolMember,
  CapacityPoolObservation,
  CapacityQuantityObservation,
  CapacityResponseMeta,
  CapacitySourceCoverage,
} from "@skyhook-io/k8s-ui";
import { CapacityView } from "./CapacityView";
import { updateDemandSearchParam } from "./CapacityDemand";

vi.mock("../../context/ConnectionContext", () => ({
  useConnection: () => ({ connection: { state: "connected" } }),
}));

const generatedAt = "2026-07-13T08:00:00Z";

function sourceCoverage(
  status: CapacitySourceCoverage["status"] = "available",
  reason?: string,
  scope: CapacitySourceCoverage["scope"] = "cluster",
): CapacitySourceCoverage {
  return {
    status,
    reason,
    scope,
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
    nodeClassKinds: [
      {
        group: "karpenter.k8s.aws",
        kind: "EC2NodeClass",
        resource: "ec2nodeclasses",
        versions: ["v1"],
      },
    ],
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
  certainty: CapacityQuantityObservation["certainty"] = "exact",
): CapacityQuantityObservation {
  return {
    resources,
    certainty,
    sources: ["karpenter.status"],
    asOf: generatedAt,
    granularity: "aggregate",
  };
}

function boundedMeta(count: number): CapacityBoundedResultMeta {
  return { total: count, returned: count, truncated: false };
}

const poolSummary: CapacityOverviewResponse["pools"][number] = {
  resource: {
    ref: { group: "karpenter.sh", kind: "NodePool", name: "default" },
    apiVersion: "karpenter.sh/v1",
  },
  mode: "dynamic",
  ready: true,
  nodeClass: {
    group: "karpenter.k8s.aws",
    kind: "EC2NodeClass",
    name: "general",
  },
  ledger: {
    configuredLimit: quantity({ cpu: "20", memory: "80Gi" }),
    provisioned: quantity({ cpu: "10", memory: "40Gi" }),
    scheduledRequests: quantity({ cpu: "6500m", memory: "28Gi" }),
    actualUsage: {
      quantity: quantity({ cpu: "4", memory: "16Gi" }),
      coveredNodes: 4,
      totalNodes: 4,
      coveredAllocatable: quantity({ cpu: "10", memory: "40Gi" }),
      utilization: [
        { resource: "cpu", usage: "4", allocatable: "10", percent: 40 },
        { resource: "memory", usage: "16Gi", allocatable: "40Gi", percent: 40 },
      ],
    },
    limitPressure: [
      {
        resource: "cpu",
        provisioned: "10",
        limit: "20",
        percent: 50,
        overLimit: false,
      },
    ],
  },
  claims: {
    total: 4,
    pending: 0,
    launched: 4,
    registered: 4,
    initialized: 4,
    ready: 4,
    failed: 0,
    terminating: 0,
  },
  nodes: { total: 4, ready: 4, notReady: 0, cordoned: 0, terminating: 0 },
  issueCount: 1,
  factCount: 2,
};

function overview(
  overrides: Partial<CapacityOverviewResponse> = {},
): CapacityOverviewResponse {
  return {
    ...meta,
    state: "available",
    summary: {
      actions: [
        {
          code: "configured_limit_pressure",
          count: 1,
          highestSeverity: "warning",
          pools: [poolSummary.resource],
          truncated: false,
        },
      ],
      aggregateDemand: quantity({ cpu: "6", memory: "24Gi" }),
      scheduling: {
        scheduledRequests: quantity({
          cpu: "21900m",
          memory: "79Gi",
          pods: "27",
        }),
        allocatable: quantity({ cpu: "31200m", memory: "102Gi", pods: "220" }),
        inFlightCapacity: quantity({ cpu: "4" }),
      },
      claimStages: {
        total: 4,
        pending: 0,
        launched: 0,
        registered: 0,
        initialized: 0,
        ready: 3,
        failed: 1,
        terminating: 0,
      },
      poolCount: 1,
      claimCount: 4,
      nodeCount: 4,
      pendingPodCount: 2,
      managers: [],
    },
    pools: [poolSummary],
    poolsTruncated: false,
    groups: [],
    orphanAutoscalerGroups: [],
    orphanAutoscalerGroupsMeta: boundedMeta(0),
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Comprehensive-capacity fixtures: multi-manager groups, autoscaler children,
// orphan (scale-to-zero) groups, managers rollup, cluster scheduling.
// ---------------------------------------------------------------------------

const managers: CapacityManagerSummary[] = [
  { manager: "karpenter", groupCount: 1, status: "healthy" },
  {
    manager: "gke_autoscaler",
    groupCount: 1,
    status: "degraded",
    detail: "1 group backing off on a stockout",
  },
];

const gkeChild: CapacityAutoscalerChildObservation = {
  id: "gke-mig-us-central1-a",
  name: "https://www.googleapis.com/.../gke-mig-us-central1-a",
  minSize: 1,
  maxSize: 10,
  target: 3,
  health: "healthy",
  readyNodes: 3,
  totalNodes: 3,
  asOf: generatedAt,
};

const gkeChildBackoff: CapacityAutoscalerChildObservation = {
  id: "gke-mig-us-central1-b",
  name: "https://www.googleapis.com/.../gke-mig-us-central1-b",
  minSize: 0,
  maxSize: 5,
  target: 2,
  health: "degraded",
  readyNodes: 1,
  totalNodes: 2,
  backoff: {
    errorClass: "OutOfResource",
    errorCode: "stockout",
    errorMessage: "Instances unavailable in zone us-central1-b",
  },
  asOf: generatedAt,
};

const karpenterGroup: CapacityGroupSummary = {
  id: "grp-karpenter-default",
  platform: "karpenter",
  // Matches poolSummary.resource.ref.name so the karpenter row can join pools.
  name: "default",
  manager: "karpenter",
  managerValidated: true,
  nodeCount: 4,
  readyNodeCount: 4,
  allocatable: quantity({ cpu: "10", memory: "40Gi" }),
  scheduledRequests: quantity({ cpu: "6500m", memory: "28Gi" }),
  scaling: [{ code: "dynamic", summary: "provisions on demand" }],
  children: [],
  childrenMeta: boundedMeta(0),
};

const gkeGroup: CapacityGroupSummary = {
  id: "grp-gke-default",
  platform: "gke",
  name: "gke-default-pool",
  manager: "gke_autoscaler",
  managerValidated: true,
  nodeCount: 5,
  readyNodeCount: 4,
  allocatable: quantity({ cpu: "20", memory: "80Gi" }),
  scheduledRequests: quantity({ cpu: "12", memory: "40Gi" }),
  scaling: [
    { code: "min_max", summary: "1–10 nodes across 2 zones" },
    { code: "surge", summary: "surge upgrade" },
  ],
  children: [gkeChild, gkeChildBackoff],
  childrenMeta: { total: 3, returned: 2, truncated: true },
};

// A group whose allocatable vector carries the "unlabeled soup" the cell must
// tame: cpu + memory lead, a GPU is promoted, pods + ephemeral-storage collapse.
const richGroup: CapacityGroupSummary = {
  id: "grp-rich",
  platform: "karpenter",
  name: "rich-pool",
  manager: "karpenter",
  managerValidated: true,
  nodeCount: 3,
  readyNodeCount: 3,
  allocatable: quantity({
    cpu: "16",
    memory: "64Gi",
    pods: "990",
    "ephemeral-storage": "200Gi",
    "nvidia.com/gpu": "8",
  }),
  scheduledRequests: quantity({ cpu: "8", memory: "32Gi" }),
  scaling: [{ code: "dynamic", summary: "provisions on demand" }],
  children: [],
  childrenMeta: boundedMeta(0),
};

const orphanScaleToZero: CapacityAutoscalerChildObservation = {
  id: "gke-scale-to-zero",
  name: "https://www.googleapis.com/.../gke-scale-to-zero",
  minSize: 0,
  maxSize: 8,
  // A real published zero — scale-to-zero, not an absent value.
  target: 0,
  health: "healthy",
  // asOf deliberately omitted → "not probed".
};

function comprehensiveOverview(): CapacityOverviewResponse {
  const base = overview();
  return {
    ...base,
    summary: {
      ...base.summary,
      managers,
      clusterScheduling: {
        scheduledRequests: quantity({ cpu: "30", memory: "120Gi" }),
        allocatable: quantity({ cpu: "60", memory: "240Gi" }),
      },
      unattributedNodeCount: 2,
    },
    groups: [karpenterGroup, gkeGroup],
    orphanAutoscalerGroups: [orphanScaleToZero],
    orphanAutoscalerGroupsMeta: boundedMeta(1),
  };
}

const poolDetail: CapacityPoolObservation = {
  resource: poolSummary.resource,
  generation: 3,
  observedGeneration: 3,
  createdAt: generatedAt,
  updatedAt: generatedAt,
  mode: "dynamic",
  ready: true,
  conditions: [{ type: "Ready", status: "True" }],
  nodeClass: {
    reference: {
      group: "karpenter.k8s.aws",
      kind: "EC2NodeClass",
      name: "general",
    },
    ready: true,
    conditions: [{ type: "Ready", status: "True" }],
  },
  configuration: {
    weight: 10,
    labels: { team: "platform" },
    requirements: [
      {
        key: "karpenter.sh/capacity-type",
        operator: "In",
        values: ["spot", "on-demand"],
      },
    ],
    taints: [],
    startupTaints: [],
    expireAfter: "720h",
  },
  ledger: poolSummary.ledger,
  disruption: {
    consolidationPolicy: "WhenEmptyOrUnderutilized",
    consolidateAfter: "30s",
    budgets: [],
  },
  claims: poolSummary.claims,
  nodes: poolSummary.nodes,
  issues: [],
  facts: [],
  coverage: {
    ...meta.coverage,
    nodes: sourceCoverage("denied", "Node inventory hidden by permissions"),
    pods: sourceCoverage("denied", "Pod inventory hidden by permissions"),
    workloads: sourceCoverage(
      "denied",
      "Workload attribution hidden by permissions",
    ),
  },
};

function poolDetailResponse(
  pool: CapacityPoolObservation | undefined,
): CapacityPoolDetailResponse {
  return { ...meta, state: "available", pool };
}

const cleanPoolDetail: CapacityPoolObservation = {
  ...poolDetail,
  workloads: {
    scheduledPodCount: 12,
    workloadCount: 3,
    topScheduled: [
      {
        owner: { kind: "Deployment", namespace: "payments", name: "checkout" },
        podCount: 6,
        requests: quantity({ cpu: "6", memory: "24Gi" }),
      },
    ],
    topScheduledMeta: boundedMeta(1),
    pendingEligibleGroupCount: 0,
  },
  coverage: meta.coverage,
};

const nodeMember: CapacityPoolMember = {
  type: "node",
  resource: { ref: { kind: "Node", name: "ip-10-0-1-1" } },
  node: {
    ready: true,
    cordoned: false,
    conditions: [],
    instanceType: "m6i.large",
    capacityType: "on-demand",
    zone: "us-east-1a",
    architecture: "amd64",
    allocatable: quantity({ cpu: "2", memory: "8Gi" }),
    scheduledRequests: quantity({ cpu: "1", memory: "4Gi" }),
    podCount: 7,
  },
};

const claimMember: CapacityPoolMember = {
  type: "claim",
  resource: { ref: { kind: "NodeClaim", name: "default-xyz9k" } },
  claim: {
    stage: "ready",
    conditions: [{ type: "Ready", status: "True" }],
    nodeName: "ip-10-0-1-1",
    capacity: quantity({ cpu: "2", memory: "8Gi" }),
  },
};

function memberResponse(
  type: CapacityMemberListResponse["type"],
  items: CapacityPoolMember[],
  coverage: CapacityMemberListResponse["coverage"] = meta.coverage,
): CapacityMemberListResponse {
  return {
    ...meta,
    coverage,
    state: "available",
    pool: poolSummary.resource,
    type,
    items,
    page: { hasMore: false },
  };
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
    constraints: [
      {
        predicate: "nodeSelector",
        key: "kubernetes.io/arch",
        operator: "In",
        values: ["amd64"],
        sourcePath: "spec.nodeSelector",
      },
    ],
    constraintsMeta: boundedMeta(1),
    tolerations: [],
    tolerationsMeta: boundedMeta(0),
  },
  state: "awaiting_capacity",
  schedulerReasons: [
    {
      code: "Unschedulable",
      source: "scheduler",
      message: "0/4 nodes are available",
      count: 3,
      firstSeen: generatedAt,
      lastSeen: generatedAt,
    },
  ],
  schedulerReasonsMeta: boundedMeta(1),
  poolEvaluations: [
    {
      pool: { kind: "NodePool", name: "default" },
      result: "declared_compatible",
      evidence: [
        {
          predicate: "requirements",
          sourcePath: "spec.requirements",
          observedValues: ["amd64"],
          expectedValues: ["amd64"],
          confidence: "high",
          explanation: "architecture requirement matches",
        },
      ],
      evidenceMeta: boundedMeta(1),
      unknownPredicates: [],
      unknownPredicatesMeta: boundedMeta(0),
    },
  ],
  poolEvaluationsMeta: boundedMeta(1),
  poolEvaluationCounts: { declaredCompatible: 1, incompatible: 0, unknown: 0 },
  issues: [],
};

// Rollup deliberately differs from the single-group page (which has 3 pods in
// 1 group) so tests prove counts come from the server summary, not the page.
const demandSummary: CapacityDemandSummary = {
  total: { podCount: 27, groupCount: 9 },
  byState: {
    waiting_for_scheduler: { podCount: 3, groupCount: 1 },
    held: { podCount: 2, groupCount: 2 },
    awaiting_capacity: { podCount: 12, groupCount: 3 },
    blocked: { podCount: 10, groupCount: 3 },
    unknown: { podCount: 0, groupCount: 0 },
  },
};

function demandResponse(
  overrides: Partial<CapacityDemandResponse> = {},
): CapacityDemandResponse {
  return {
    ...meta,
    state: "available",
    summary: demandSummary,
    items: [demandGroup],
    page: { hasMore: false },
    ...overrides,
  };
}

function poolListResponse(
  names: string[],
  page: CapacityPoolListResponse["page"] = { hasMore: false },
): CapacityPoolListResponse {
  return {
    ...meta,
    state: "available",
    items: names.map((name) => ({
      ...poolSummary,
      resource: {
        ...poolSummary.resource,
        ref: { ...poolSummary.resource.ref, name },
      },
    })),
    page,
  };
}

const activityEpisode: CapacityActivityEpisode = {
  id: "ep-1",
  type: "provision",
  state: "completed",
  summary: "Provisioned a node for pending pods",
  primaryReasonCode: "Launched",
  startedAt: generatedAt,
  endedAt: generatedAt,
  durationSeconds: 120,
  pool: { ref: { kind: "NodePool", name: "default" } },
  evidence: [
    {
      at: generatedAt,
      source: "k8s_event",
      reasonCode: "Launched",
      rawReason: "Launched",
      rawMessage: "created a NodeClaim",
      relationship: "direct",
      confidence: "high",
      refs: [],
    },
  ],
  evidenceMeta: boundedMeta(1),
};

function activityResponse(): CapacityActivityResponse {
  return {
    ...meta,
    state: "available",
    items: [activityEpisode],
    page: { hasMore: false },
    cursorStatus: "valid",
    observation: {
      startedAt: generatedAt,
      endedAt: generatedAt,
      sources: ["k8s_event"],
      retention: {
        mode: "bounded_local_timeline",
        maxEvents: 2000,
        maxAgeSeconds: 86400,
      },
      gaps: [],
    },
    aggregate: {
      total: 3,
      byType: {
        provision: { total: 2, byState: { completed: 1, failed: 1 } },
        disruption: { total: 1, byState: { blocked: 1 } },
      },
    },
  };
}

function renderCapacity(
  path: string,
  seed: (client: QueryClient) => void,
): string {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, retryOnMount: false } },
  });
  seed(client);
  return renderToString(
    <MemoryRouter initialEntries={[path]}>
      <QueryClientProvider client={client}>
        <CapacityView onOpenResource={() => {}} />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("CapacityView overview", () => {
  it("renders KPI tiles, the node-groups inventory, and the Karpenter chip", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], overview()),
    );
    expect(html).toContain("Capacity overview");
    expect(html).toContain("Karpenter");
    expect(html).toContain("NodeClaims");
    expect(html).toContain("Pending pods");
    // The former "NodePools" tile is now the unified "Node groups" tile.
    expect(html).toContain("Node groups");
    expect(html).toContain("Operational signals");
    expect(html).toContain("default");
  });

  it("draws cluster scheduling capacity with honest scale semantics", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], overview()),
    );
    expect(html).toContain("Karpenter scheduling capacity");
    expect(html).toContain("not usage, not a health score");
    expect(html).toContain("in flight (beyond the edge)");
    expect(html).toContain("pending demand — count, not to scale");
    expect(html).toContain("3 ready · 1 failed");
    expect(html).toContain("View inventory ↓");
  });

  it("frames the aggregate demand as scheduling demand, not usage", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], overview()),
    );
    expect(html).toContain(
      "Active pending requests (scheduling demand, not usage):",
    );
    expect(html).toContain("Pod slots 2");
  });

  it("draws the negative-priority sub-band and legend under exact certainty", () => {
    const base = overview();
    const data: CapacityOverviewResponse = {
      ...base,
      summary: {
        ...base.summary,
        scheduling: {
          scheduledRequests: quantity({ cpu: "6", memory: "24Gi", pods: "12" }),
          allocatable: quantity({ cpu: "12", memory: "48Gi", pods: "110" }),
          negativePriorityRequests: quantity({ cpu: "3" }),
        },
      },
    };
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], data),
    );
    // The legend entry only appears when a proportional band is actually drawn.
    expect(html).toContain("negative-priority (potential preemption victims)");
  });

  it("annotates negative-priority as a lower bound instead of a band under non-exact certainty", () => {
    const base = overview();
    const data: CapacityOverviewResponse = {
      ...base,
      summary: {
        ...base.summary,
        scheduling: {
          scheduledRequests: quantity(
            { cpu: "6", memory: "24Gi", pods: "12" },
            "lower_bound",
          ),
          allocatable: quantity(
            { cpu: "12", memory: "48Gi", pods: "110" },
            "lower_bound",
          ),
          negativePriorityRequests: quantity({ cpu: "3" }, "lower_bound"),
        },
      },
    };
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], data),
    );
    // No proportional split under a lower bound — a ≥ annotation instead, and
    // no band means no legend swatch describing one.
    expect(html).toContain("≥3 cores");
    expect(html).toContain("negative-priority");
    expect(html).not.toContain("(potential preemption victims)");
  });

  it("derives operational signals from server actions", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], overview()),
    );
    expect(html).toContain("Configured limit pressure");
    expect(html).toContain("Warn");
  });

  it("makes the outside-pools note agree with how many facts it lists", () => {
    const base = overview();
    const single = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], {
        ...base,
        summary: {
          ...base.summary,
          actions: [],
          orphanedClaimCount: 0,
          unpooledNodeCount: 9,
        },
      }),
    );
    expect(single).toContain("9 nodes are outside Karpenter pools");
    expect(single).toContain("This may be intentional.");
    expect(single).not.toContain("Both may be intentional.");

    const both = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], {
        ...base,
        summary: {
          ...base.summary,
          actions: [],
          orphanedClaimCount: 2,
          unpooledNodeCount: 9,
        },
      }),
    );
    expect(both).toContain("Both may be intentional.");
  });

  it("marks pending pods unavailable — never zero — when pod access is denied", () => {
    const denied = overview({
      summary: { ...overview().summary, pendingPodCount: undefined },
      coverage: {
        ...meta.coverage,
        pods: sourceCoverage("denied", "Pod inventory hidden by permissions"),
      },
    });
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], denied),
    );
    expect(html).toContain("Pod access denied — not zero");
    expect(html).toContain("Unavailable — Pod access denied");
  });

  it("labels namespace-scoped data as a lower bound", () => {
    const scoped = overview({
      coverage: {
        ...meta.coverage,
        pods: {
          ...sourceCoverage("available", undefined, "explicit_namespaces"),
          namespaces: ["payments", "media"],
        },
      },
    });
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], scoped),
    );
    expect(html).toContain("Explicit namespaces (2)");
    expect(html).toContain("Namespaced data is a lower bound");
  });

  it("shows the integration state returned by the server", () => {
    // not_detected only shows the block when there is no observable surface —
    // strip node coverage and managers so nothing is left to render.
    const notDetected = renderCapacity("/capacity", (client) =>
      client.setQueryData(
        ["capacity", "overview"],
        overview({
          state: "not_detected",
          summary: { ...overview().summary, managers: [] },
          coverage: {
            ...meta.coverage,
            nodes: sourceCoverage("unavailable"),
            nodePools: sourceCoverage("unavailable"),
          },
        }),
      ),
    );
    expect(notDetected).toContain("Karpenter not detected");

    // denied likewise only blocks when the node fleet is invisible too — with
    // no observable surface, the access-denied block stands.
    const deniedState = renderCapacity("/capacity", (client) =>
      client.setQueryData(
        ["capacity", "overview"],
        overview({
          state: "denied",
          summary: { ...overview().summary, managers: [] },
          groups: [],
          coverage: {
            ...meta.coverage,
            nodes: sourceCoverage("denied"),
            nodePools: sourceCoverage("denied"),
          },
        }),
      ),
    );
    expect(deniedState).toContain("Capacity access denied");
  });

  it("renders the cluster-only overview when NodePools are denied but nodes are visible", () => {
    // Karpenter denied, node fleet visible: the Overview softens the denial into
    // the cluster-only shape (inventory + managers from nodes/labels) with an
    // honest notice, never the access-denied block and never Karpenter sections.
    const denied = overview({
      state: "denied",
      summary: {
        ...overview().summary,
        managers: [
          { manager: "gke_autoscaler", groupCount: 1, status: "healthy" },
        ],
        poolCount: 0,
      },
      pools: [],
      groups: [gkeGroup],
      coverage: {
        ...meta.coverage,
        nodePools: sourceCoverage("denied", "NodePools hidden by permissions"),
      },
    });
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], denied),
    );
    expect(html).not.toContain("Capacity access denied");
    expect(html).toContain("Karpenter view unavailable");
    expect(html).toContain("Node groups");
    expect(html).toContain("gke-default-pool");
    expect(html).toContain("GKE autoscaler");
    // Karpenter-specific surfaces stay hidden under denial.
    expect(html).not.toContain("Open Demand");
  });

  it("explains why Karpenter data is still syncing", () => {
    const syncing = renderCapacity("/capacity", (client) =>
      client.setQueryData(
        ["capacity", "overview"],
        overview({
          state: "syncing",
          coverage: {
            ...meta.coverage,
            nodePools: {
              ...sourceCoverage("syncing"),
              reasonCode: "karpenter_discovery_partial",
            },
          },
        }),
      ),
    );
    expect(syncing).toContain(
      "Karpenter API discovery is incomplete; retrying…",
    );
  });

  it("renders the capacity managers tile with labels, counts, and degraded detail", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], comprehensiveOverview()),
    );
    expect(html).toContain("Capacity managers");
    expect(html).toContain("Karpenter");
    expect(html).toContain("GKE autoscaler");
    expect(html).toContain("1 group");
    // Degraded rollup surfaces its reason as a visible line (SSR-safe).
    expect(html).toContain("1 group backing off on a stockout");
  });

  it("shows autoscaler detection unavailable — never 'none' — when the status ConfigMap is denied", () => {
    const denied = overview({
      summary: { ...overview().summary, managers: [] },
      coverage: {
        ...meta.coverage,
        autoscalerStatus: sourceCoverage(
          "denied",
          "Autoscaler status hidden by permissions",
        ),
      },
    });
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], denied),
    );
    expect(html).toContain("Detection unavailable");
    expect(html).not.toContain("None detected");
  });

  it("renders a unified node-groups table across managers with karpenter pool affordances", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], comprehensiveOverview()),
    );
    expect(html).toContain("Node groups");
    // GKE group: name, manager badge, and server-rendered scaling prose.
    expect(html).toContain("gke-default-pool");
    expect(html).toContain("GKE autoscaler");
    expect(html).toContain("1–10 nodes across 2 zones");
    expect(html).toContain("provisions on demand");
    // Ready/total node counts render as "ready/total".
    expect(html).toContain("4/5");
    // Karpenter row keeps its pool-detail + raw-inspect affordances.
    expect(html).toContain("default");
    expect(html).toContain("Inspect");
    // Unattributed footer — a bucket, never called "static".
    expect(html).toContain("2 nodes without group identity");
  });

  it("expands autoscaler child sub-tables with min–max, backoff, and truncation", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], comprehensiveOverview()),
    );
    // Collapse keeps children mounted, so SSR renders the sub-table content.
    expect(html).toContain("gke-mig-us-central1-a");
    expect(html).toContain("gke-mig-us-central1-b");
    expect(html).toContain("1–10");
    // Backoff badge label is visible (its message rides a hover tooltip).
    expect(html).toContain("stockout");
    expect(html).toContain("Showing 2 of 3 child groups.");
  });

  it("renders scale-to-zero orphan groups with a real 0 target and 'not probed' freshness", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], comprehensiveOverview()),
    );
    expect(html).toContain("Known to the autoscaler, unattributed");
    expect(html).toContain("gke-scale-to-zero");
    // A nil asOf reads "not probed", never a zero time or "now".
    expect(html).toContain("not probed");
  });

  it("titles the scheduling bar cluster-wide when clusterScheduling is present", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], comprehensiveOverview()),
    );
    expect(html).toContain("Cluster scheduling capacity");
    expect(html).toContain(
      "scheduled requests vs node allocatable, all observed nodes",
    );
    expect(html).toContain("in flight (Karpenter, beyond the edge)");
  });

  it("keeps the scheduling bar Karpenter-scoped when clusterScheduling is absent", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], overview()),
    );
    expect(html).toContain("Karpenter scheduling capacity");
    expect(html).toContain("in flight (beyond the edge)");
  });

  it("renders the overview on a Karpenter-less cluster that still has observable capacity", () => {
    const notDetected = comprehensiveOverview();
    notDetected.state = "not_detected";
    // Karpenter-less: no NodePools, no Karpenter pool ledger, no claims — only
    // node/manager evidence remains.
    notDetected.pools = [];
    notDetected.poolsTruncated = false;
    notDetected.summary = {
      ...notDetected.summary,
      poolCount: 0,
      claimCount: undefined,
      claimStages: undefined,
      scheduling: undefined,
      managers: [
        { manager: "gke_autoscaler", groupCount: 1, status: "healthy" },
      ],
      nodeCount: 9,
    };
    notDetected.groups = [gkeGroup];

    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], notDetected),
    );
    // The page renders instead of the not-detected block.
    expect(html).not.toContain("Karpenter not detected");
    expect(html).toContain("Capacity overview");
    // Node-groups inventory + managers tile both render from node/CM evidence.
    expect(html).toContain("Node groups");
    expect(html).toContain("gke-default-pool");
    expect(html).toContain("Capacity managers");
    expect(html).toContain("GKE autoscaler");
    // Cluster scheduling bar renders from clusterScheduling.
    expect(html).toContain("Cluster scheduling capacity");
    // Karpenter-only signals section is omitted (no dead-end Demand/Activity links).
    expect(html).not.toContain("Operational signals");
  });

  it("says Karpenter is not detected rather than calling its inventory unavailable", () => {
    // A cluster with no Karpenter CRDs at all: nodes are fully observed and
    // carry no group-identity labels, so there is nothing we failed to read.
    const notDetected = overview({
      state: "not_detected",
      pools: [],
      groups: [],
      coverage: {
        ...meta.coverage,
        nodePools: sourceCoverage("unavailable"),
      },
    });
    notDetected.summary = {
      ...notDetected.summary,
      poolCount: undefined,
      claimCount: undefined,
      claimStages: undefined,
      scheduling: undefined,
      nodeCount: 9,
    };
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], notDetected),
    );
    expect(html).toContain("Karpenter not detected");
    // "Unavailable" would claim we tried to look and could not.
    expect(html).not.toContain("NodePool inventory is unavailable");
  });

  it("keeps the not-detected block when the cluster exposes no observable capacity surface", () => {
    const blank = overview({
      state: "not_detected",
      summary: { ...overview().summary, managers: [], nodeCount: undefined },
      groups: [],
      orphanAutoscalerGroups: [],
      coverage: {
        ...meta.coverage,
        nodes: sourceCoverage("unavailable"),
        nodePools: sourceCoverage("unavailable"),
      },
    });
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], blank),
    );
    expect(html).toContain("Karpenter not detected");
    // The block replaces the page — no tiles, no managers surface.
    expect(html).not.toContain("Capacity managers");
  });

  it("headlines the Nodes tile with the total and decomposes pooled vs unattributed", () => {
    const decomposed = overview({
      summary: {
        ...overview().summary,
        nodeCount: 17,
        poolCount: 3,
        unpooledNodeCount: 5,
        unattributedNodeCount: 2,
      },
    });
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], decomposed),
    );
    // Headline is the total observed, not the pooled subset (the old bug showed
    // pooled = 12 as the headline on a 17-node cluster).
    expect(html).toContain(">17</div>");
    expect(html).not.toContain(">12</div>");
    // Subline decomposes: 17 − 5 unpooled = 12 in Karpenter pools · 2 unattributed.
    expect(html).toContain("12 in Karpenter pools");
    expect(html).toContain("2 unattributed");
  });

  it("promotes GPUs into the inventory cell and collapses other resources into +N more", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(
        ["capacity", "overview"],
        overview({ groups: [richGroup] }),
      ),
    );
    // CPU + memory lead; the GPU is promoted into the visible cell.
    expect(html).toContain("16 cores");
    expect(html).toContain("64.0 GiB");
    expect(html).toContain("gpu 8");
    // pods + ephemeral-storage collapse behind a labeled "+N more" tooltip.
    expect(html).toContain("+2 more");
    // The unlabeled "990" (pods) is no longer a bare number in the cell — it
    // lives inside the tooltip, which SSR does not paint.
    expect(html).not.toContain("990");
  });

  it("calls autoscaler detection unavailable for every unreadable source, not just denied", () => {
    for (const reasonCode of [
      "autoscaler_status_cache_scope",
      "autoscaler_status_cache_unavailable",
      "autoscaler_status_read_failed",
      "autoscaler_status_parse_failed",
    ]) {
      const base = overview();
      const html = renderCapacity("/capacity", (client) =>
        client.setQueryData(["capacity", "overview"], {
          ...base,
          summary: { ...base.summary, managers: [] },
          coverage: {
            ...meta.coverage,
            autoscalerStatus: {
              ...sourceCoverage("unavailable"),
              reasonCode,
            },
          },
        }),
      );
      expect(html).toContain("Detection unavailable");
      expect(html).not.toContain("None detected");
    }
  });

  it("never concludes 'none detected' anywhere on the page while detection is unavailable", () => {
    // The tile was fixed first; the group row's Manager column reached the same
    // conclusion in lowercase. Assert over the WHOLE page, both capitalizations.
    const managerlessGroup: CapacityGroupSummary = {
      ...gkeGroup,
      manager: undefined,
      children: [],
      childrenMeta: boundedMeta(0),
      scaling: [
        {
          code: "manager_detection_unavailable",
          summary: "manager detection unavailable",
        },
      ],
    };
    for (const coverage of [
      sourceCoverage("denied", "Autoscaler status hidden by permissions"),
      {
        ...sourceCoverage("unavailable"),
        reasonCode: "autoscaler_status_cache_scope",
      },
      {
        ...sourceCoverage("unavailable"),
        reasonCode: "autoscaler_status_cache_unavailable",
      },
      {
        ...sourceCoverage("error"),
        reasonCode: "autoscaler_status_read_failed",
      },
      {
        ...sourceCoverage("unavailable"),
        reasonCode: "autoscaler_status_parse_failed",
      },
    ]) {
      const base = overview();
      const html = renderCapacity("/capacity", (client) =>
        client.setQueryData(["capacity", "overview"], {
          ...base,
          summary: { ...base.summary, managers: [] },
          groups: [managerlessGroup],
          coverage: { ...meta.coverage, autoscalerStatus: coverage },
        }),
      );
      expect(html).not.toMatch(/none detected/i);
      expect(html).toContain("Detection unavailable");
      expect(html).toContain("detection unavailable");
      // The Scaling column's server-authored fact must not contradict the
      // Manager column beside it.
      expect(html).toContain("manager detection unavailable");
    }
  });

  it("shows certainty glyphs only on deviation — exact is the quiet default", () => {
    const base = overview();
    const clean = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], base),
    );
    // Fully-observed tiles carry no = chip; the aria notes disappear with them.
    expect(clean).not.toContain("Node inventory observed");

    const bounded = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], {
        ...base,
        coverage: {
          ...meta.coverage,
          pods: sourceCoverage("available", undefined, "all_authorized_namespaces"),
        },
      }),
    );
    // Deviation still announces itself.
    expect(bounded).toContain("≥");
  });

  it("the pending tile never dead-ends on a Karpenter-less cluster", () => {
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], (() => {
        const base = overview({ state: "not_detected", pools: [] });
        return {
          ...base,
          summary: {
            ...base.summary,
            poolCount: undefined,
            claimCount: undefined,
            managers: [],
          },
        };
      })()),
    );
    expect(html).toContain("View issues");
    expect(html).not.toContain("Open Demand");
  });

  it("splits platform identity from autoscaler observation when the source was read", () => {
    const managerlessGroup: CapacityGroupSummary = {
      ...gkeGroup,
      manager: undefined,
      children: [],
      childrenMeta: boundedMeta(0),
    };
    const base = overview();
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], {
        ...base,
        summary: { ...base.summary, managers: [] },
        groups: [managerlessGroup],
        coverage: {
          ...meta.coverage,
          autoscalerStatus: {
            ...sourceCoverage("unavailable"),
            reasonCode: "autoscaler_status_not_published",
            namespaces: ["kube-system"],
          },
        },
      }),
    );
    // Tile: the autoscaler was read and published nothing — an honest "none".
    expect(html).toContain("None detected");
    // Row: the group's identity CAME from a platform label; concluding "none
    // detected" there would read as failing to recognize the fleet's most
    // basic fact. Platform identity renders instead.
    expect(html).toContain("GKE node pool");
    expect(html).not.toContain("none detected");
    expect(html).not.toContain("detection unavailable");
  });

  it("says none detected only when the autoscaler was read and published nothing", () => {
    const base = overview();
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], {
        ...base,
        summary: { ...base.summary, managers: [] },
        coverage: {
          ...meta.coverage,
          autoscalerStatus: {
            ...sourceCoverage("unavailable"),
            reasonCode: "autoscaler_status_not_published",
            // The server reports the namespace it probed on this path.
            namespaces: ["kube-system"],
          },
        },
      }),
    );
    expect(html).toContain("None detected");
    expect(html).not.toContain("Detection unavailable");
    // "None detected" is only evidence about the namespace probed.
    expect(html).toContain("No cluster-autoscaler-status ConfigMap in");
    expect(html).toContain("kube-system");
  });

  it("dates the managers tile with the autoscaler payload's own timestamp", () => {
    const base = comprehensiveOverview();
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], {
        ...base,
        coverage: { ...meta.coverage, autoscalerStatus: sourceCoverage() },
      }),
    );
    expect(html).toContain("as of ");
  });

  it("hedges the node-groups count when NodePools are unreadable", () => {
    const base = overview();
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], {
        ...base,
        // A denied NodePool source hides scale-to-zero groups entirely, so the
        // count is a floor even with every node observed.
        summary: { ...base.summary, poolCount: undefined },
        state: "denied",
        pools: [],
        groups: [gkeGroup],
        coverage: {
          ...meta.coverage,
          nodePools: sourceCoverage(
            "denied",
            "NodePools hidden by permissions",
          ),
        },
      }),
    );
    expect(html).toContain("≥");
    expect(html).toContain("NodePool groups holding no nodes are invisible");
    // An omitted poolCount is never rendered as zero Karpenter pools.
    expect(html).not.toContain("0 Karpenter");
  });

  it("keeps a detail-less backoff visible instead of dashing it out", () => {
    // The legacy status format reports backoff with no error class, code or
    // message; a dash there would read as "not backing off".
    const base = comprehensiveOverview();
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], {
        ...base,
        groups: [
          {
            ...gkeGroup,
            children: [{ ...gkeChild, backoff: {} }],
            childrenMeta: boundedMeta(1),
          },
        ],
      }),
    );
    // Match the badge itself — "Backoff" also appears as the column header and
    // the group-row rollup, neither of which proves the cell kept the signal.
    expect(html).toMatch(/badge-sm[^"]*">Backoff</);
  });

  it("marks a group whose manager attribution is unvalidated", () => {
    const base = comprehensiveOverview();
    const html = renderCapacity("/capacity", (client) =>
      client.setQueryData(["capacity", "overview"], {
        ...base,
        groups: [{ ...gkeGroup, managerValidated: false }],
      }),
    );
    expect(html).toContain("unvalidated");
  });
});

describe("CapacityView pool detail", () => {
  it("renders the capacity ledger with headers and a certainty legend", () => {
    const html = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse(cleanPoolDetail),
      ),
    );
    expect(html).toContain("Capacity ledger");
    expect(html).toContain("Configured limit");
    expect(html).toContain("Node allocatable");
    expect(html).toContain("Unallocated");
    expect(html).toContain("CPU");
    expect(html).toContain("Memory");
    expect(html).toContain("Certainty glyphs:");
  });

  it("keeps pod-derived ledger and attribution unavailable under denied pod access", () => {
    const html = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse(poolDetail),
      ),
    );
    expect(html).toContain("Unavailable");
    expect(html).toContain("Unknown — Pod access denied");
    expect(html).toContain("Unavailable — Pod access denied");
  });

  it("explains a missing pool observation without implying zero", () => {
    const html = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse(undefined),
      ),
    );
    expect(html).toContain("NodePool unavailable");
  });

  it("collapses the NodeClass cascade to its root and tucks raw conditions behind a toggle", () => {
    const issue = (reason: string, message: string) => ({
      id: reason,
      severity: "warning" as const,
      source: "condition" as const,
      category: "node_provisioning_failure",
      category_group: "capacity",
      grouping_scope: "unknown" as const,
      group: "karpenter.sh",
      kind: "NodePool",
      name: "default",
      reason,
      message,
    });
    const brokenRaw = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse({
          ...cleanPoolDetail,
          issues: [
            issue("NodeClassNotReady", "ValidationSucceeded=False"),
            issue("NodePoolNotReady", "NodeClassReady=False"),
          ],
          conditions: [
            { type: "Ready", status: "False", reason: "UnhealthyDependents" },
            { type: "ValidationSucceeded", status: "True" },
          ],
        }),
      ),
    );
    const broken = brokenRaw.replace(/<!-- -->/g, "");
    // The pool-not-ready row is a derivable echo of the NodeClass issue beside
    // it — the header chip already carries the state, so the row disappears.
    expect(broken).not.toContain("NodeClassReady=False");
    expect(broken).toContain("ValidationSucceeded=False");
    // Raw conditions collapse behind the toggle while issues cover them.
    expect(broken).toContain("Show controller conditions (2)");
    expect(broken).not.toContain("UnhealthyDependents");

    const standaloneRaw = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse({
          ...cleanPoolDetail,
          issues: [issue("NodePoolNotReady", "NodeClassReady=False")],
        }),
      ),
    );
    const standalone = standaloneRaw.replace(/<!-- -->/g, "");
    // Without the NodeClass issue the pool-unready row is novel — it stays.
    expect(standalone).toContain("NodeClassReady=False");

    const healthyRaw = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse(cleanPoolDetail),
      ),
    );
    const healthy = healthyRaw.replace(/<!-- -->/g, "");
    // No issues: the raw conditions ARE the content and stay visible.
    expect(healthy).toContain("Controller conditions");
    expect(healthy).not.toContain("Show controller conditions");
  });

  it("deep-links a provisioning-health issue into the pool's provision episodes", () => {
    const html = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse({
          ...cleanPoolDetail,
          issues: [
            {
              id: "reg-unhealthy",
              severity: "warning",
              source: "condition",
              category: "node_provisioning_failure",
              category_group: "capacity",
              grouping_scope: "unknown",
              group: "karpenter.sh",
              kind: "NodePool",
              name: "default",
              reason: "NodePoolNodeRegistrationUnhealthy",
              message: "NodeClass is in Ready=Unknown",
            },
            {
              id: "nodeclass-not-ready",
              severity: "warning",
              source: "condition",
              category: "node_provisioning_failure",
              category_group: "capacity",
              grouping_scope: "unknown",
              group: "karpenter.k8s.aws",
              kind: "EC2NodeClass",
              name: "gpu",
              reason: "NodeClassNotReady",
              message: "Validation failed",
            },
          ],
        }),
      ),
    );
    const links = html.split("View provisioning episodes").length - 1;
    expect(links).toBe(1);
  });

  it("leads with the diagnosis when the pool has issues, ledger when clean", () => {
    const broken = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse({
          ...cleanPoolDetail,
          issues: [
            {
              id: "nodepool-not-ready",
              severity: "warning",
              source: "condition",
              category: "node_provisioning_failure",
              category_group: "capacity",
              grouping_scope: "unknown",
              group: "karpenter.sh",
              kind: "NodePool",
              name: "default",
              reason: "NodePoolNotReady",
              message: "Validation failed",
            },
          ],
        }),
      ),
    );
    expect(broken.indexOf("Issues &amp; conditions")).toBeGreaterThan(-1);
    expect(broken.indexOf("Issues &amp; conditions")).toBeLessThan(
      broken.indexOf("Capacity ledger"),
    );

    const clean = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse(cleanPoolDetail),
      ),
    );
    expect(clean.indexOf("Capacity ledger")).toBeLessThan(
      clean.indexOf("Issues &amp; conditions"),
    );
  });

  it("narrates the zero-node ledger instead of presenting a wall of zeros", () => {
    const html = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse({
          ...cleanPoolDetail,
          nodes: { total: 0, ready: 0, notReady: 0, cordoned: 0, terminating: 0 },
        }),
      ),
    );
    expect(html).toContain("No nodes exist in this pool right now");
    const populated = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse(cleanPoolDetail),
      ),
    );
    expect(populated).not.toContain("No nodes exist in this pool right now");
  });

  it("renders structured issue diagnosis in pool posture", () => {
    const html = renderCapacity("/capacity/pools/default/posture", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse({
          ...cleanPoolDetail,
          issues: [
            {
              id: "nodepool-not-ready",
              severity: "warning",
              source: "condition",
              category: "node_provisioning_failure",
              category_group: "capacity",
              grouping_scope: "unknown",
              group: "karpenter.sh",
              kind: "NodePool",
              name: "default",
              reason: "NodePoolNotReady",
              message: "Validation failed",
              cause: "The NodePool Ready condition is false.",
              action:
                "Inspect the Ready condition and correct the referenced configuration.",
              count: 3,
              members: [
                { kind: "NodeClaim", name: "default-failed-a" },
                { kind: "NodeClaim", name: "default-failed-b" },
              ],
              members_truncated: true,
            },
            {
              id: "nodeclass-not-ready",
              severity: "warning",
              source: "condition",
              category: "node_provisioning_failure",
              category_group: "capacity",
              grouping_scope: "unknown",
              group: "karpenter.k8s.aws",
              kind: "EC2NodeClass",
              name: "loadtest",
              reason: "NodeClassNotReady",
              message: "Subnets could not be resolved",
              cause: "The EC2NodeClass Ready condition is false.",
              action: "Inspect the EC2NodeClass selector terms.",
              count: 0,
              members: [],
            },
          ],
        }),
      ),
    );
    expect(html).toContain("NodePool Not Ready");
    expect(html).toContain("Cause:");
    expect(html).toContain("The NodePool Ready condition is false.");
    expect(html).toContain("Next:");
    expect(html).toContain("Inspect the Ready condition");
    expect(html).toMatch(/3(?:<!-- -->)? affected/);
    expect(html).toMatch(
      /NodeClaim(?:<!-- -->)?\/(?:<!-- -->)?default-failed-a/,
    );
    expect(html).toContain("Showing 2 of 3 affected resources.");
    expect(html).toContain("Inspect nodes &amp; claims →");
    expect(html).toMatch(
      /Inspect (?:<!-- -->)?EC2NodeClass(?:<!-- -->)?\/(?:<!-- -->)?loadtest/,
    );
  });

  it("loads node and claim members from the URL-backed members tab", () => {
    const html = renderCapacity("/capacity/pools/default/members", (client) => {
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse(cleanPoolDetail),
      );
      client.setQueryData(
        ["capacity", "pool", "default", "members", "node", 50, undefined],
        memberResponse("node", [nodeMember]),
      );
      client.setQueryData(
        ["capacity", "pool", "default", "members", "claim", 50, undefined],
        memberResponse("claim", [claimMember]),
      );
    });
    expect(html).toContain("Nodes");
    expect(html).toContain("NodeClaims");
    expect(html).toContain("ip-10-0-1-1");
    expect(html).toContain("default-xyz9k");
  });

  it("keeps member pod counts unknown under partial RBAC", () => {
    const html = renderCapacity("/capacity/pools/default/members", (client) => {
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse(cleanPoolDetail),
      );
      client.setQueryData(
        ["capacity", "pool", "default", "members", "node", 50, undefined],
        memberResponse("node", [nodeMember], {
          ...meta.coverage,
          pods: sourceCoverage("denied", "Pod inventory hidden by permissions"),
        }),
      );
      client.setQueryData(
        ["capacity", "pool", "default", "members", "claim", 50, undefined],
        memberResponse("claim", [claimMember]),
      );
    });
    expect(html).toContain("Unknown");
  });

  it("hedges a member's pod-derived requests when pod coverage is a lower bound", () => {
    const html = renderCapacity("/capacity/pools/default/members", (client) => {
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse(cleanPoolDetail),
      );
      client.setQueryData(
        ["capacity", "pool", "default", "members", "node", 50, undefined],
        memberResponse(
          "node",
          [
            {
              ...nodeMember,
              node: {
                ...nodeMember.node!,
                scheduledRequests: quantity(
                  { cpu: "1", memory: "4Gi" },
                  "lower_bound",
                ),
              },
            },
          ],
          {
            ...meta.coverage,
            pods: sourceCoverage(
              "available",
              undefined,
              "all_authorized_namespaces",
            ),
          },
        ),
      );
      client.setQueryData(
        ["capacity", "pool", "default", "members", "claim", 50, undefined],
        memberResponse("claim", [claimMember]),
      );
    });
    // Namespace-limited pod coverage cannot produce a bare exact-looking value.
    expect(html).toContain("≥");
  });

  it("hedges the scheduled pod count under namespace-scoped pod coverage", () => {
    const html = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse({
          ...cleanPoolDetail,
          coverage: {
            ...meta.coverage,
            pods: sourceCoverage(
              "available",
              undefined,
              "all_authorized_namespaces",
            ),
          },
        }),
      ),
    );
    expect(html).toContain("Scheduled pods on this pool");
    expect(html).toMatch(/≥ (?:<!-- -->)?12/);
  });

  it("names the upper bound in the ledger certainty legend", () => {
    const html = renderCapacity("/capacity/pools/default", (client) =>
      client.setQueryData(
        ["capacity", "pool", "default"],
        poolDetailResponse(cleanPoolDetail),
      ),
    );
    expect(html).toContain("≤");
    expect(html).toContain("upper bound");
  });
});

describe("CapacityView demand", () => {
  it("never mixes the global denominator into an owner-scoped header", () => {
    const html = renderCapacity(
      "/capacity/demand?owner=payments%2FDeployment%2Fcheckout",
      (client) =>
        client.setQueryData(
          [
            "capacity",
            "demand",
            25,
            undefined,
            undefined,
            undefined,
            "payments/Deployment/checkout",
            undefined,
          ],
          demandResponse(),
        ),
    );
    expect(html).toContain("for this workload");
    expect(html).not.toContain("showing");
    expect(html).toContain(
      "the state counts below describe all observed demand",
    );
  });

  it("headlines the dominant reason when every pool rules the group out", () => {
    const blockedGroup = {
      ...demandGroup,
      poolEvaluationCounts: { declaredCompatible: 0, incompatible: 2, unknown: 0 },
      poolEvaluations: [
        {
          pool: { kind: "NodePool", name: "general" },
          result: "incompatible" as const,
          evidence: [
            {
              predicate: "selector.gpu",
              sourcePath: "spec.nodeSelector",
              observedValues: ["gpu=true"],
              expectedValues: [],
              confidence: "high" as const,
              explanation: "requires label gpu=true, which no pool requirement or template declares",
            },
          ],
          evidenceMeta: boundedMeta(1),
          unknownPredicates: [],
          unknownPredicatesMeta: boundedMeta(0),
        },
        {
          pool: { kind: "NodePool", name: "spot" },
          result: "incompatible" as const,
          evidence: [
            {
              predicate: "selector.gpu",
              sourcePath: "spec.nodeSelector",
              observedValues: ["gpu=true"],
              expectedValues: [],
              confidence: "high" as const,
              explanation: "requires label gpu=true, which no pool requirement or template declares",
            },
          ],
          evidenceMeta: boundedMeta(1),
          unknownPredicates: [],
          unknownPredicatesMeta: boundedMeta(0),
        },
      ],
    };
    const html = renderCapacity("/capacity/demand", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse({ items: [blockedGroup] }),
      ),
    );
    expect(html).toContain("No pool can take this group");
    expect(html).toContain("requires label gpu=true");
    expect(html).toContain("(2 of 2 incompatible pools)");

    const mixed = renderCapacity("/capacity/demand", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse({
          items: [
            {
              ...blockedGroup,
              poolEvaluationCounts: { declaredCompatible: 1, incompatible: 1, unknown: 0 },
            },
          ],
        }),
      ),
    );
    expect(mixed).not.toContain("No pool can take this group");
  });

  it("celebrates the unfiltered empty state instead of blaming inactive filters", () => {
    const html = renderCapacity("/capacity/demand", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse({ items: [] }),
      ),
    );
    expect(html).toContain("Nothing is pending");
    expect(html).toContain("Every pod the scheduler knows about is placed");
    expect(html).not.toContain("match these filters");
  });

  it("claims only what it can see when pod coverage is namespace-bounded", () => {
    const html = renderCapacity("/capacity/demand", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse({
          items: [],
          coverage: {
            ...meta.coverage,
            pods: sourceCoverage("available", undefined, "all_authorized_namespaces"),
          },
        }),
      ),
    );
    expect(html).toContain("No pending demand observed");
    expect(html).not.toContain("Every pod the scheduler knows about is placed");
  });

  it("names the state when a state filter empties the list, and success copy under pool-only", () => {
    const stateFiltered = renderCapacity("/capacity/demand?state=blocked", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, "blocked", undefined, undefined, undefined],
        demandResponse({ items: [] }),
      ),
    );
    expect(stateFiltered).toContain("No groups in");
    expect(stateFiltered).toContain("the pills above carry the counts");

    const poolOnly = renderCapacity("/capacity/demand?pool=general", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, "general", undefined, undefined],
        demandResponse({ items: [] }),
      ),
    );
    expect(poolOnly).toContain("Nothing is pending");
    expect(poolOnly).not.toContain("match these filters");
  });

  it("lands the pod-drawer bridge on the pod's workload, filtered and labeled", () => {
    const html = renderCapacity("/capacity/demand?pod=payments%2Fcheckout-abc12", (client) =>
      client.setQueryData(
        [
          "capacity",
          "demand",
          25,
          undefined,
          undefined,
          undefined,
          undefined,
          "payments/checkout-abc12",
        ],
        demandResponse(),
      ),
    );
    expect(html).toContain("pod checkout-abc12&#x27;s workload");
    expect(html).toContain("Show all demand");
    expect(html).toContain("for this workload");
    expect(html).not.toContain("showing");
  });

  it("reports a true zero when the bridged pod has no pending demand", () => {
    const html = renderCapacity("/capacity/demand?pod=payments%2Fcheckout-abc12", (client) =>
      client.setQueryData(
        [
          "capacity",
          "demand",
          25,
          undefined,
          undefined,
          undefined,
          undefined,
          "payments/checkout-abc12",
        ],
        { ...demandResponse(), items: [] },
      ),
    );
    expect(html).toContain("No pending demand for pod checkout-abc12&#x27;s workload");
    expect(html).toContain("true zero");
  });

  it("groups pending pods by scheduling signature with pool evaluations", () => {
    const html = renderCapacity("/capacity/demand", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse(),
      ),
    );
    expect(html).toContain("Demand");
    expect(html).toContain("Pending pods grouped by scheduling signature");
    expect(html).toContain("checkout");
    expect(html).toContain("Pool evaluations");
    expect(html).toContain(
      "declared compatibility, not a scheduling guarantee",
    );
  });

  it("shows structured capacity-linked issues on the demand group", () => {
    const html = renderCapacity("/capacity/demand", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse({
          items: [
            {
              ...demandGroup,
              issues: [
                {
                  id: "capacity-unschedulable",
                  severity: "warning",
                  source: "scheduling",
                  category: "unschedulable",
                  category_group: "scheduling",
                  grouping_scope: "workload",
                  group: "apps",
                  kind: "Deployment",
                  namespace: "payments",
                  name: "checkout",
                  reason: "NodePoolConstraintMismatch",
                  cause:
                    "The pending pods require a NodePool label no compatible pool declares.",
                  action:
                    "Review the workload selector or add a compatible NodePool.",
                  capacity_relevant: true,
                  count: 2,
                  members: [
                    {
                      kind: "Pod",
                      namespace: "payments",
                      name: "checkout-abc",
                    },
                    {
                      kind: "Pod",
                      namespace: "payments",
                      name: "checkout-def",
                    },
                  ],
                },
              ],
            },
          ],
        }),
      ),
    );
    expect(html).toContain("Capacity diagnosis");
    expect(html).toContain("NodePool Constraint Mismatch");
    expect(html).toContain("Cause:");
    expect(html).toContain("Review the workload selector");
    expect(html).toMatch(/2(?:<!-- -->)? affected/);
    expect(html).toMatch(/Pod(?:<!-- -->)?\/(?:<!-- -->)?checkout-def/);
  });

  it("takes pill and header counts from the server summary, not the page", () => {
    const html = renderCapacity("/capacity/demand", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse(),
      ),
    );
    // Page holds 1 group / 3 pods; summary says 27 total, 12 awaiting, 10 blocked.
    expect(html).toContain("All states · 27");
    expect(html).toContain("Awaiting capacity · 12");
    expect(html).toContain("Blocked · 10");
    // Reconcile header uses the summary total as the denominator, not the page.
    expect(html).toContain("showing 3 of 27 pending pods in 1 of 9 groups");
  });

  it("never fabricates zeros when the server omits the summary", () => {
    const html = renderCapacity("/capacity/demand", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse({ summary: undefined }),
      ),
    );
    // Pills render as plain labels — no "· N" count, never a fabricated "· 0".
    expect(html).toContain("All states");
    expect(html).not.toContain("All states ·");
    expect(html).not.toContain("Blocked ·");
    // No authoritative total is claimed from page-local counts.
    expect(html).not.toContain("showing");
    expect(html).toContain("Pending pods grouped by scheduling signature");
  });

  it("offers a URL-backed NodePool evaluation perspective", () => {
    const html = renderCapacity(
      "/capacity/demand?state=blocked&pool=spot",
      (client) => {
        client.setQueryData(
          ["capacity", "demand", 25, undefined, "blocked", "spot", undefined, undefined],
          demandResponse(),
        );
        client.setQueryData(
          ["capacity", "pools", 100, undefined],
          poolListResponse(["default", "spot"]),
        );
      },
    );
    expect(html).toContain("Evaluate against");
    expect(html).toContain("All NodePools");
    expect(html).toContain('<option value="spot" selected="">spot</option>');
    expect(html).toContain("Evaluated against NodePool");
  });

  it("keeps paginated NodePool discovery available beyond the first 100 pools", () => {
    const names = Array.from(
      { length: 100 },
      (_, index) => `pool-${String(index).padStart(3, "0")}`,
    );
    const html = renderCapacity("/capacity/demand", (client) => {
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse(),
      );
      client.setQueryData(
        ["capacity", "pools", 100, undefined],
        poolListResponse(names, { hasMore: true, nextCursor: "pool-099" }),
      );
    });
    expect(html).toContain("pool-000");
    expect(html).toContain("pool-099");
    expect(html).toContain("Load more NodePools…");
  });

  it("does not block Demand when NodePool option discovery fails", () => {
    const html = renderCapacity("/capacity/demand", (client) => {
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse(),
      );
      const poolQuery = client.getQueryCache().build(client, {
        queryKey: ["capacity", "pools", 100, undefined],
        queryFn: async () => poolListResponse([]),
      });
      poolQuery.setState({
        ...poolQuery.state,
        error: new Error("pool list unavailable"),
        errorUpdateCount: 1,
        errorUpdatedAt: Date.now(),
        fetchStatus: "idle",
        status: "error",
      });
    });
    expect(html).toContain("checkout");
    expect(html).toContain("All NodePools");
    expect(html).toContain(
      "NodePool options unavailable; demand remains available.",
    );
  });

  it("hedges the state pills when pod coverage is a lower bound", () => {
    const html = renderCapacity("/capacity/demand", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse({
          coverage: {
            ...meta.coverage,
            pods: sourceCoverage(
              "available",
              undefined,
              "all_authorized_namespaces",
            ),
          },
        }),
      ),
    );
    // Groups outside the authorized namespaces are invisible, so the rollup
    // counts are floors, not totals.
    expect(html).toContain("Blocked · ≥10");
    expect(html).toContain("All states · ≥27");
  });

  it("leads the signature card with the grouping basis, not the hash", () => {
    const html = renderCapacity("/capacity/demand", (client) =>
      client.setQueryData(
        ["capacity", "demand", 25, undefined, undefined, undefined, undefined, undefined],
        demandResponse(),
      ),
    );
    expect(html).toContain(
      "Grouped by identical per-pod requests · 1 selector",
    );
    // The fingerprint hash moved into the tooltip, which SSR does not paint.
    expect(html).not.toContain("sched-fp:abc123");
  });

  it("preserves state while changing or clearing pool perspective", () => {
    const selected = updateDemandSearchParam("?state=blocked", "pool", "spot");
    expect(new URLSearchParams(selected).get("state")).toBe("blocked");
    expect(new URLSearchParams(selected).get("pool")).toBe("spot");

    const cleared = updateDemandSearchParam(selected, "pool", undefined);
    expect(new URLSearchParams(cleared).get("state")).toBe("blocked");
    expect(new URLSearchParams(cleared).has("pool")).toBe(false);
  });
});

describe("CapacityView activity", () => {
  it("renders the window line, type rollup pills, and demoted provenance", () => {
    const html = renderCapacity("/capacity/activity", (client) =>
      client.setQueryData(
        [
          "capacity",
          "activity",
          50,
          undefined,
          undefined,
          undefined,
          undefined,
          undefined,
          undefined,
        ],
        activityResponse(),
      ),
    );
    expect(html).toContain("Activity");
    expect(html).toContain("Recent Karpenter capacity events");
    // The observation-window card collapsed into a single header line.
    expect(html).not.toContain("Observation window");
    expect(html).toContain("Window ");
    expect(html).toContain("Provisioned a node for pending pods");
    // Type pills carry whole-window rollup counts, failures called out.
    expect(html).toContain("All · 3");
    expect(html).toContain("Provision · 2 · 1 failed");
    expect(html).toContain("Disruption · 1");
    // Provenance columns hide behind the toggle; the raw evidence leads.
    expect(html).toContain("Show provenance");
    expect(html).not.toContain("Relationship");
    expect(html).toContain("Raw");
  });

  it("filters live through the shared SearchBox and pool selector — no Apply submit", () => {
    const html = renderCapacity("/capacity/activity", (client) =>
      client.setQueryData(
        [
          "capacity",
          "activity",
          50,
          undefined,
          undefined,
          undefined,
          undefined,
          undefined,
          undefined,
        ],
        activityResponse(),
      ),
    );
    // The search is the shared SearchBox with an example placeholder — not a
    // submit-gated <input>.
    expect(html).toContain("LaunchFailed, node name");
    // NodePool filter is the shared PoolSelector with an "Any pool" empty option.
    expect(html).toContain("Any pool");
    // The Apply-to-commit flow is gone.
    expect(html).not.toContain("Apply");
  });

  it("search filters the loaded episodes client-side — never a server fetch", () => {
    // The seeded query key carries no search term: `q` narrows the already
    // loaded page in the client, so typing can never unmount the view or
    // trigger a refetch.
    const matched = renderCapacity("/capacity/activity?q=nodeclaim", (client) =>
      client.setQueryData(
        [
          "capacity",
          "activity",
          50,
          undefined,
          undefined,
          undefined,
          undefined,
          undefined,
          undefined,
        ],
        activityResponse(),
      ),
    );
    // Matches evidence text ("created a NodeClaim"), case-insensitively.
    expect(matched).toContain("Provisioned a node for pending pods");

    const unmatched = renderCapacity(
      "/capacity/activity?q=no-such-episode",
      (client) =>
        client.setQueryData(
          [
            "capacity",
            "activity",
            50,
            undefined,
            undefined,
            undefined,
            undefined,
            undefined,
            undefined,
          ],
          activityResponse(),
        ),
    );
    expect(unmatched).not.toContain("Provisioned a node for pending pods");
    expect(unmatched).toContain("No loaded episodes match this search");
    // Type pills keep the whole-window rollup — search narrows the page, not
    // the window.
    expect(unmatched).toContain("All · 3");
  });

  it("hedges the rollup pills when either evidence source is partial", () => {
    const partial: Partial<CapacityActivityResponse["coverage"]>[] = [
      // Episodes are correlated from Karpenter's object events as well as the
      // retained timeline; a bound on either bounds every rollup count.
      {
        karpenterObjectEvents: sourceCoverage(
          "available",
          undefined,
          "all_authorized_namespaces",
        ),
      },
      { timeline: sourceCoverage("partial") },
    ];
    for (const coverage of partial) {
      const html = renderCapacity("/capacity/activity", (client) =>
        client.setQueryData(
          [
            "capacity",
            "activity",
            50,
            undefined,
            undefined,
            undefined,
            undefined,
            undefined,
            undefined,
          ],
          {
            ...activityResponse(),
            coverage: { ...meta.coverage, ...coverage },
          },
        ),
      );
      expect(html).toContain("All · ≥3");
      expect(html).toContain("Provision · ≥2 · ≥1 failed");
    }
  });

  it("leaves the rollup pills exact when both evidence sources are clean", () => {
    const html = renderCapacity("/capacity/activity", (client) =>
      client.setQueryData(
        [
          "capacity",
          "activity",
          50,
          undefined,
          undefined,
          undefined,
          undefined,
          undefined,
          undefined,
        ],
        activityResponse(),
      ),
    );
    expect(html).toContain("All · 3");
    expect(html).not.toContain("All · ≥3");
  });
});
