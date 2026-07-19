import { useQuery } from "@tanstack/react-query";
import type { APIResource } from "../types";
import { apiUrl, getAuthHeaders, getCredentialsMode } from "./config";

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

async function fetchJSON<T>(path: string): Promise<T> {
  const response = await fetch(apiUrl(path), {
    credentials: getCredentialsMode(),
    headers: getAuthHeaders(),
  });
  if (!response.ok) {
    const error = await response
      .json()
      .catch(() => ({ error: "Unknown error" }));
    throw new Error(error.error || `HTTP ${response.status}`);
  }
  return response.json();
}

// Fetch all API resources from the cluster
export function useAPIResources() {
  return useQuery<APIResource[]>({
    queryKey: ["api-resources"],
    queryFn: () => fetchJSON("/api-resources"),
    staleTime: 5 * 60 * 1000, // 5 minutes - resources don't change often
  });
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

// Nav/palette gating for the Capacity view. The per-request capability is
// authoritative when it has resolved discovery + RBAC: available shows,
// denied/not_detected hides (an RBAC-denied user gets a 403 page otherwise).
// While the capability is still syncing pre-discovery (cluster connecting),
// fall back to the discovery signal to avoid hiding/flashing the nav item.
export function karpenterNavVisible(
  capability: { state: string } | undefined,
  apiResources: APIResource[] | undefined,
): boolean {
  if (capability?.state === "available") return true;
  if (capability?.state === "denied" || capability?.state === "not_detected")
    return false;
  return hasKarpenterNodePools(apiResources);
}
