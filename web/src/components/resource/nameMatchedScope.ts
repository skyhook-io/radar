import { isClusterScopeWithheld, type ClusterScope } from "@skyhook-io/k8s-ui/types/cluster-scope";

/**
 * How the Pod-name-matched charts in one section were kept to this cluster.
 * `caveat` keeps the "may include another cluster" wording: an unlabeled
 * store, older Radar (no clusterScope), or a chart that hasn't answered yet.
 */
export type NameMatchedScope = "scoped" | "asserted" | "withheld" | "caveat";

// Least certain first. A chart drawn without a cluster check outranks a
// withheld one: it shows numbers that may mix clusters, the withheld one none.
const RANK: NameMatchedScope[] = ["caveat", "withheld", "asserted", "scoped"];

function classify(scope: ClusterScope | null | undefined): NameMatchedScope {
  if (scope?.state === "scoped_configured" || scope?.state === "scoped_verified") return "scoped";
  if (scope?.state === "single_cluster_asserted") return "asserted";
  return isClusterScopeWithheld(scope) ? "withheld" : "caveat";
}

/** The least certain scope among a section's charts. */
export function leastCertainScope(scopes: (ClusterScope | null | undefined)[]): NameMatchedScope {
  if (scopes.length === 0) return "caveat";
  return scopes.map(classify).reduce((a, b) => (RANK.indexOf(a) <= RANK.indexOf(b) ? a : b));
}

export const NAME_MATCHED_SUMMARY: Record<NameMatchedScope, string> = {
  scoped: "matched by cluster label",
  asserted: "operator declared single-cluster",
  withheld: "identity unverified",
  caveat: "identity unverified",
};

/** The sentence that replaces "may include another cluster", or undefined to keep it. */
export const NAME_MATCHED_CLUSTER_NOTE: Record<NameMatchedScope, string | undefined> = {
  scoped: "They're kept to this cluster by its cluster labels.",
  asserted: "An operator declared this metrics store single-cluster, so they don't include another cluster.",
  withheld: "Charts that couldn't be kept to this cluster say why in place of their data.",
  caveat: undefined,
};
