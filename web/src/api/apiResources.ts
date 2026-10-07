import { useEffect } from "react";
import { initNavigationMap } from "@skyhook-io/k8s-ui/utils/navigation";
import { useQuery } from "@tanstack/react-query";
import type { APIResource } from "../types";
import { apiUrl, getAuthHeaders, getCredentialsMode } from "./config";
import { readErrorBody } from "./httpErrors";

// Re-export pure functions from package
export {
  categorizeResources,
  CORE_RESOURCES,
  findAPIResourceForRoute,
  formatGroupName,
  shortenGroupName,
  getKindLabel,
  getKindPlural,
} from "@skyhook-io/k8s-ui";
export type { ResourceCategory } from "@skyhook-io/k8s-ui";

async function fetchJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(apiUrl(path), {
    signal,
    credentials: getCredentialsMode(),
    headers: getAuthHeaders(),
  });
  if (!response.ok) {
    const error = await readErrorBody(response);
    throw new Error(error.error || `HTTP ${response.status}`);
  }
  return response.json();
}

// Known GitOps navigation identities, not installation or CRD-existence evidence.
export const GITOPS_KINDS: APIResource[] = [
  { name: 'applications', kind: 'Application', group: 'argoproj.io', version: 'v1alpha1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'applicationsets', kind: 'ApplicationSet', group: 'argoproj.io', version: 'v1alpha1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'appprojects', kind: 'AppProject', group: 'argoproj.io', version: 'v1alpha1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'kustomizations', kind: 'Kustomization', group: 'kustomize.toolkit.fluxcd.io', version: 'v1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'helmreleases', kind: 'HelmRelease', group: 'helm.toolkit.fluxcd.io', version: 'v2', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'gitrepositories', kind: 'GitRepository', group: 'source.toolkit.fluxcd.io', version: 'v1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'ocirepositories', kind: 'OCIRepository', group: 'source.toolkit.fluxcd.io', version: 'v1beta2', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'helmrepositories', kind: 'HelmRepository', group: 'source.toolkit.fluxcd.io', version: 'v1', namespaced: true, verbs: ['list', 'get'], isCrd: true },
  { name: 'alerts', kind: 'Alert', group: 'notification.toolkit.fluxcd.io', version: 'v1beta3', namespaced: true, verbs: ['list', 'get'], isCrd: true },
]


// Fetch all API resources from the cluster
export function useAPIResources() {
  const result = useQuery<APIResource[]>({
    queryKey: ["api-resources"],
    queryFn: ({ signal }) => fetchJSON("/api-resources", signal),
    staleTime: 5 * 60 * 1000, // 5 minutes - resources don't change often
  });
  useEffect(() => {
    const actual = result.isError ? [] : result.data ?? [];
    const knownOnly = GITOPS_KINDS.filter(known => !actual.some(resource =>
      resource.group === known.group && (resource.kind === known.kind || resource.name === known.name)));
    initNavigationMap([...actual, ...knownOnly]);
  }, [result.data, result.isError]);
  return result;
}

// The report families the server watches (reportGroups in
// internal/k8s/policy_reports.go): the wgpolicyk8s.io working-group API and
// its openreports.io successor, which names the same resources differently.
const POLICY_REPORT_RESOURCES: Record<string, readonly string[]> = {
  "wgpolicyk8s.io": ["policyreports", "clusterpolicyreports"],
  "openreports.io": ["reports", "clusterreports"],
};

export function hasPolicyReports(resources: APIResource[] | undefined): boolean {
  return (
    resources?.some((resource) =>
      POLICY_REPORT_RESOURCES[resource.group]?.includes(resource.name),
    ) ?? false
  );
}

export function hasKarpenterNodePools(
  resources: APIResource[] | undefined,
): boolean {
  return (
    resources?.some(
      (resource) =>
        resource.name === "nodepools" &&
        resource.group === "karpenter.sh" &&
        resource.verbs?.includes("list"),
    ) ?? false
  );
}

// Whether the Capacity screens are reachable for this caller — the gate on
// links and entry points into them. The per-request capability is authoritative
// once it has resolved discovery + RBAC: available opens, denied/not_detected
// closes (an RBAC-denied user would otherwise land on a 403 page). While the
// capability is still syncing pre-discovery (cluster connecting), fall back to
// the discovery signal so entry points don't flicker.
export function karpenterCapacityAvailable(
  capability: { state: string } | undefined,
  apiResources: APIResource[] | undefined,
): boolean {
  if (capability?.state === "available") return true;
  if (capability?.state === "denied" || capability?.state === "not_detected")
    return false;
  return hasKarpenterNodePools(apiResources);
}
