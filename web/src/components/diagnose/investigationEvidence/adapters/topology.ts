import type { Topology } from "@skyhook-io/k8s-ui";
import type {
  InvestigationEvidenceSource,
  InvestigationTopologyEdge,
  InvestigationTopologyNode,
} from "../types";
import {
  type ProjectionBuilder,
  invalidPayload,
  nonEmptyString,
  record,
  relevanceForResource,
  resourceRef,
  scopeFromArgs,
  stringArray,
} from "../builder";
import { addNarrowHint } from "../observations";

function topologyNode(value: unknown): InvestigationTopologyNode | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.id) ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name)
  ) {
    return undefined;
  }
  return candidate as unknown as InvestigationTopologyNode;
}

function topologyEdge(value: unknown): InvestigationTopologyEdge | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.source) ||
    !nonEmptyString(candidate.target) ||
    !nonEmptyString(candidate.type)
  ) {
    return undefined;
  }
  return candidate as unknown as InvestigationTopologyEdge;
}

type TopologyPartiality = {
  warnings: string[];
  largeCluster: boolean;
  hiddenKinds: string[];
  requiresNamespaceFilter: boolean;
  crdDiscoveryStatus?: NonNullable<Topology["crdDiscoveryStatus"]>;
  estimatedNodes?: number;
  summaryMode: boolean;
};

/**
 * Both get_topology wire shapes carry the same completeness metadata. Keep the
 * adapter strict: silently dropping a malformed flag would make a partial graph
 * look complete in Evidence.
 */
function topologyPartiality(
  value: Record<string, unknown>,
): TopologyPartiality | undefined {
  const warnings =
    value.warnings === undefined ? [] : stringArray(value.warnings);
  const hiddenKinds =
    value.hiddenKinds === undefined ? [] : stringArray(value.hiddenKinds);
  const discovery = value.crdDiscoveryStatus;
  const estimatedNodes = value.estimatedNodes;
  if (
    !warnings ||
    !hiddenKinds ||
    (value.largeCluster !== undefined &&
      typeof value.largeCluster !== "boolean") ||
    (value.requiresNamespaceFilter !== undefined &&
      typeof value.requiresNamespaceFilter !== "boolean") ||
    (value.summaryMode !== undefined &&
      typeof value.summaryMode !== "boolean") ||
    (discovery !== undefined &&
      discovery !== "idle" &&
      discovery !== "discovering" &&
      discovery !== "ready") ||
    (estimatedNodes !== undefined &&
      (typeof estimatedNodes !== "number" ||
        !Number.isSafeInteger(estimatedNodes) ||
        estimatedNodes < 0))
  ) {
    return undefined;
  }
  return {
    warnings,
    largeCluster: value.largeCluster === true,
    hiddenKinds,
    requiresNamespaceFilter: value.requiresNamespaceFilter === true,
    crdDiscoveryStatus: discovery as
      NonNullable<Topology["crdDiscoveryStatus"]> | undefined,
    estimatedNodes,
    summaryMode: value.summaryMode === true,
  };
}

function addTopologyLimitations(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  partiality: TopologyPartiality,
): void {
  for (const warning of partiality.warnings) {
    const normalized = warning.toLowerCase();
    builder.limit(
      source,
      "Topology coverage",
      warning,
      normalized.includes("large graph") || normalized.includes("too large")
        ? "truncated"
        : "unknown",
    );
  }

  const scaleDetails: string[] = [];
  const estimate = partiality.estimatedNodes
    ? ` (about ${partiality.estimatedNodes} estimated nodes)`
    : "";
  if (partiality.requiresNamespaceFilter) {
    scaleDetails.push(
      `The all-namespace topology was not built because the cluster is too large${estimate}; run a namespace-scoped topology search to collect a smaller graph.`,
    );
  } else if (partiality.largeCluster) {
    scaleDetails.push(
      `Large-cluster optimizations were active${estimate}; high-cardinality detail may be grouped.`,
    );
  }
  if (partiality.hiddenKinds.length > 0) {
    scaleDetails.push(
      `Resource kinds omitted by the large-cluster optimization: ${partiality.hiddenKinds.join(", ")}.`,
    );
  }
  if (partiality.summaryMode) {
    scaleDetails.push(
      "Summary mode collapsed individual Pods into workload or Service counts.",
    );
  }
  if (scaleDetails.length > 0) {
    builder.limit(
      source,
      "Topology scale",
      scaleDetails.join(" "),
      "truncated",
    );
  }

  if (
    partiality.crdDiscoveryStatus === "idle" ||
    partiality.crdDiscoveryStatus === "discovering"
  ) {
    builder.limit(
      source,
      "Custom Resource topology",
      partiality.crdDiscoveryStatus === "idle"
        ? "Custom Resource discovery had not started when this topology was captured; Custom Resource nodes and relationships may be missing."
        : "Custom Resource discovery was still in progress when this topology was captured; Custom Resource nodes and relationships may be missing.",
      "unknown",
    );
  }
}

export function adaptNeighborhood(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const root = resourceRef(value?.root);
  const subgraph = record(value?.subgraph);
  if (
    !value ||
    !root ||
    !subgraph ||
    !Array.isArray(subgraph.nodes) ||
    !Array.isArray(subgraph.edges) ||
    typeof value.truncated !== "boolean"
  ) {
    invalidPayload(builder, source);
    return;
  }
  const nodes = subgraph.nodes
    .map(topologyNode)
    .filter((item): item is InvestigationTopologyNode => Boolean(item));
  const edges = subgraph.edges
    .map(topologyEdge)
    .filter((item): item is InvestigationTopologyEdge => Boolean(item));
  if (
    nodes.length !== subgraph.nodes.length ||
    edges.length !== subgraph.edges.length
  ) {
    invalidPayload(builder, source);
    return;
  }
  addNarrowHint(builder, source, value);
  if (value.truncated === true && !nonEmptyString(value.narrowHint)) {
    builder.limit(
      source,
      "Relationships",
      "The relationship view reached its resource limit and may be incomplete.",
      "truncated",
    );
  }
  for (const raw of Array.isArray(value.omitted) ? value.omitted : []) {
    const omitted = record(raw);
    if (
      !omitted ||
      !nonEmptyString(omitted.field) ||
      !nonEmptyString(omitted.reason)
    )
      continue;
    builder.limit(
      source,
      omitted.field,
      `Relationship context omitted: ${omitted.reason.replaceAll("_", " ")}.`,
      omitted.reason === "budget_exceeded" ? "truncated" : "unknown",
    );
  }
  builder.observe(
    `relationships:${root.group ?? ""}:${root.kind}:${root.namespace ?? ""}:${root.name}`,
    "relationships",
    source,
    {
      tier: "context",
      relevance: relevanceForResource(builder, {
        kind: root.kind,
        group: root.group ?? "",
        namespace: root.namespace,
        name: root.name,
      }),
      tone: "info",
      title: `Relationships around ${root.kind} ${root.name}`,
      summary: `${nodes.length} nodes · ${edges.length} relationships`,
      data: {
        type: "relationships",
        root,
        nodes,
        edges,
        truncated: value.truncated,
      },
    },
  );
}

export function adaptTopology(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  if (!value) {
    invalidPayload(builder, source);
    return;
  }
  const partiality = topologyPartiality(value);
  if (!partiality) {
    invalidPayload(builder, source, "Topology coverage metadata");
    return;
  }
  const stats = record(value.stats);
  if (
    stats &&
    typeof stats.nodes === "number" &&
    typeof stats.edges === "number" &&
    Array.isArray(value.namespaces)
  ) {
    const namespaces = value.namespaces.flatMap((raw) => {
      const namespace = record(raw);
      const chains = stringArray(namespace?.chains);
      return namespace && nonEmptyString(namespace.namespace) && chains
        ? [{ namespace: namespace.namespace, chains }]
        : [];
    });
    if (namespaces.length !== value.namespaces.length) {
      invalidPayload(builder, source);
      return;
    }
    const problems =
      value.problems === undefined ? [] : stringArray(value.problems);
    if (!problems) {
      invalidPayload(builder, source, "Topology problems");
      return;
    }
    addTopologyLimitations(builder, source, partiality);
    builder.observe(
      `topology:${source.args ?? scopeFromArgs(source)}`,
      "topology",
      source,
      {
        tier: "context",
        relevance: "broader",
        tone: problems.length > 0 ? "warning" : "info",
        title: "Resource topology",
        summary: `${stats.nodes} nodes · ${stats.edges} relationships`,
        data: {
          type: "topology",
          stats: { nodes: stats.nodes, edges: stats.edges },
          namespaces,
          problems,
          warnings: partiality.warnings,
        },
      },
    );
    return;
  }

  if (!Array.isArray(value.nodes) || !Array.isArray(value.edges)) {
    invalidPayload(builder, source);
    return;
  }
  const nodes = value.nodes
    .map(topologyNode)
    .filter((item): item is InvestigationTopologyNode => Boolean(item));
  const edges = value.edges
    .map(topologyEdge)
    .filter((item): item is InvestigationTopologyEdge => Boolean(item));
  if (
    nodes.length !== value.nodes.length ||
    edges.length !== value.edges.length
  ) {
    invalidPayload(builder, source);
    return;
  }
  const problems = nodes
    .filter((node) => node.status === "unhealthy" || node.status === "degraded")
    .map((node) => `${node.kind} ${node.name}: ${node.status}`);
  addTopologyLimitations(builder, source, partiality);
  builder.observe(
    `topology:${source.args ?? scopeFromArgs(source)}`,
    "topology",
    source,
    {
      tier: "context",
      relevance: "broader",
      tone: problems.length > 0 ? "warning" : "info",
      title: "Resource topology",
      summary: `${nodes.length} nodes · ${edges.length} relationships`,
      data: {
        type: "topology",
        stats: { nodes: nodes.length, edges: edges.length },
        namespaces: [],
        problems,
        warnings: partiality.warnings,
      },
    },
  );
}
