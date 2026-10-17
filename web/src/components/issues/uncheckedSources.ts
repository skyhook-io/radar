import type { IssuesResponse } from "../../api/client";

// The issue sources /api/issues can list in unchecked_sources, and the line
// each one gets wherever Radar's issues would otherwise read as complete.
const SOURCE_LINES: Record<string, string> = {
  helm: "Helm releases couldn't be checked, so failed or stuck releases may be missing.",
};

// Display names for the kinds an absence entry can name.
const RESOURCE_NAMES: Record<string, string> = {
  pods: "Pods",
  networkpolicies: "NetworkPolicies",
  secrets: "Secrets",
  configmaps: "ConfigMaps",
  persistentvolumeclaims: "PersistentVolumeClaims",
  serviceaccounts: "ServiceAccounts",
  services: "Services",
  deployments: "Deployments",
  statefulsets: "StatefulSets",
  daemonsets: "DaemonSets",
  ingressclasses: "IngressClasses",
  storageclasses: "StorageClasses",
  roles: "Roles",
  clusterroles: "ClusterRoles",
};

// For a source this version doesn't know, sent by a newer Radar.
export const UNKNOWN_SOURCE_LINE = "Some issue checks couldn't run, so issues may be missing.";

function absenceLine(resources: string[], stale: boolean): string {
  const names = resources.map((r) => RESOURCE_NAMES[r] ?? r);
  const list = names.length <= 2 ? names.join(" and ") : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
  const kinds = names.length === 1 ? names[0] : "these kinds";
  const line = `Radar doesn't report missing ${list}: it can list ${kinds} but not watch them, so it can't confirm one is missing. Grant Radar watch access to ${kinds} and restart Radar to restore these checks.`;
  return stale ? `${line} Radar's copy has not refreshed for several minutes.` : line;
}

/** One line per unchecked source, known sources first, without repeats. */
export function uncheckedSourceLines(sources: IssuesResponse["unchecked_sources"]): string[] {
  const known = new Set<string>();
  const absenceResources: string[] = [];
  let absenceStale = false;
  let unknown = false;
  for (const { source, reason, resource } of sources ?? []) {
    if (source === "absence" && resource) {
      if (!absenceResources.includes(resource)) absenceResources.push(resource);
      absenceStale ||= reason === "stale";
    } else if (Object.hasOwn(SOURCE_LINES, source)) known.add(SOURCE_LINES[source]);
    else unknown = true;
  }
  const lines = [...known];
  if (absenceResources.length > 0) lines.push(absenceLine(absenceResources, absenceStale));
  return unknown ? [...lines, UNKNOWN_SOURCE_LINE] : lines;
}
