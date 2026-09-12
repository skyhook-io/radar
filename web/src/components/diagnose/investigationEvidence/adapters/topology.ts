import {
  ProjectionBuilder,
  addNarrowHint,
  addTopologyLimitations,
  invalidPayload,
  relevanceForResource,
  scopeFromArgs,
  topologyPartiality,
} from "../observations";
import {
  nonEmptyString,
  record,
  resourceRef,
  stringArray,
  topologyEdge,
  topologyNode,
} from "../parse";
import {
  type InvestigationEvidenceSource,
  type InvestigationTopologyEdge,
  type InvestigationTopologyNode,
} from "../types";

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
