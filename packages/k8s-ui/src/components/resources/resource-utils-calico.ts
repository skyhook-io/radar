import type { ResourceRef } from "../../types";

export const CALICO_GROUPS = [
  "projectcalico.org",
  "crd.projectcalico.org",
] as const;

export type CalicoApiGroup = (typeof CALICO_GROUPS)[number];

const CALICO_POLICY_KINDS = new Set([
  "networkpolicy",
  "networkpolicies",
  "globalnetworkpolicy",
  "globalnetworkpolicies",
  "stagednetworkpolicy",
  "stagednetworkpolicies",
  "stagedglobalnetworkpolicy",
  "stagedglobalnetworkpolicies",
  "stagedkubernetesnetworkpolicy",
  "stagedkubernetesnetworkpolicies",
]);

const CALICO_POLICY_LABELS: Record<string, string> = {
  networkpolicy: "CalicoNetworkPolicy",
  networkpolicies: "CalicoNetworkPolicy",
  globalnetworkpolicy: "CalicoGlobalNetworkPolicy",
  globalnetworkpolicies: "CalicoGlobalNetworkPolicy",
  stagednetworkpolicy: "CalicoStagedNetworkPolicy",
  stagednetworkpolicies: "CalicoStagedNetworkPolicy",
  stagedglobalnetworkpolicy: "CalicoStagedGlobalNetworkPolicy",
  stagedglobalnetworkpolicies: "CalicoStagedGlobalNetworkPolicy",
  stagedkubernetesnetworkpolicy: "CalicoStagedKubernetesNetworkPolicy",
  stagedkubernetesnetworkpolicies: "CalicoStagedKubernetesNetworkPolicy",
};

function apiGroup(apiVersion: unknown): string {
  if (typeof apiVersion !== "string") return "";
  const parts = apiVersion.split("/");
  return parts.length === 2 ? parts[0] : "";
}

export function isCalicoApiGroupVersion(
  apiVersion: unknown,
  group: string,
): boolean {
  return apiGroup(apiVersion) === group;
}

export function isCalicoApiGroup(group: unknown): group is CalicoApiGroup {
  return (
    typeof group === "string" &&
    (CALICO_GROUPS as readonly string[]).includes(group)
  );
}

export function isCalicoApiVersion(apiVersion: unknown): boolean {
  return isCalicoApiGroup(apiGroup(apiVersion));
}

export function getCalicoApiGroup(
  apiVersion: unknown,
): CalicoApiGroup | undefined {
  const group = apiGroup(apiVersion);
  return isCalicoApiGroup(group) ? group : undefined;
}

export function getCalicoTierRef(policy: any): ResourceRef {
  return {
    kind: "Tier",
    namespace: "",
    name: String(policy?.spec?.tier ?? "default"),
    group: getCalicoApiGroup(policy?.apiVersion),
  };
}

export function isCalicoPolicyKind(kind: unknown): boolean {
  return (
    typeof kind === "string" && CALICO_POLICY_KINDS.has(kind.toLowerCase())
  );
}

export function isCalicoNetworkPolicyKind(kind: unknown): boolean {
  return (
    typeof kind === "string" &&
    ["networkpolicy", "networkpolicies"].includes(kind.toLowerCase())
  );
}

export function isCalicoStagedKubernetesNetworkPolicyKind(kind: unknown): boolean {
  return (
    typeof kind === "string" &&
    ["stagedkubernetesnetworkpolicy", "stagedkubernetesnetworkpolicies"].includes(
      kind.toLowerCase(),
    )
  );
}

export function isCalicoPolicyResource(data: any): boolean {
  return isCalicoApiVersion(data?.apiVersion) && isCalicoPolicyKind(data?.kind);
}

export function isCoreNetworkPolicyApiVersion(apiVersion: unknown): boolean {
  return isCalicoApiGroupVersion(apiVersion, "networking.k8s.io");
}

export function isCoreNetworkPolicyResource(data: any): boolean {
  return isCoreNetworkPolicyKind(data?.kind, data?.apiVersion);
}

export function isCoreNetworkPolicyKind(
  kind: unknown,
  apiVersion: unknown,
  group?: string,
): boolean {
  if (!isCalicoNetworkPolicyKind(kind)) return false;
  if (apiVersion !== undefined && apiVersion !== null && apiVersion !== "") {
    return isCoreNetworkPolicyApiVersion(apiVersion);
  }
  return group === undefined || group === "" || group === "networking.k8s.io";
}

export function getCalicoPolicyKindLabel(kind: unknown): string {
  if (typeof kind !== "string") return "CalicoNetworkPolicy";
  return CALICO_POLICY_LABELS[kind.toLowerCase()] ?? "CalicoNetworkPolicy";
}

export function getCalicoPolicyTypes(policy: any): string[] {
  const raw = policy?.spec?.types ?? policy?.spec?.policyTypes;
  if (Array.isArray(raw)) return raw.map(String);
  return raw === undefined || raw === null || raw === "" ? [] : [String(raw)];
}

export function getCalicoPolicyRuleCount(policy: any): {
  ingress: number;
  egress: number;
} {
  return {
    ingress: Array.isArray(policy?.spec?.ingress)
      ? policy.spec.ingress.length
      : 0,
    egress: Array.isArray(policy?.spec?.egress) ? policy.spec.egress.length : 0,
  };
}

export function getCalicoPolicySelector(policy: any): string {
  const kubernetesPolicy = isCalicoStagedKubernetesNetworkPolicyKind(policy?.kind);
  const selector = kubernetesPolicy
    ? policy?.spec?.podSelector
    : policy?.spec?.selector;
  if (kubernetesPolicy) return formatKubernetesLabelSelector(selector);
  return selector === undefined || selector === null || selector === ""
    ? "all workloads"
    : String(selector);
}

export function formatKubernetesLabelSelector(selector: any): string {
  if (!selector || typeof selector !== "object") return "all workloads";
  const parts: string[] = [];
  for (const [key, value] of Object.entries(selector.matchLabels ?? {})) {
    parts.push(`${key}=${String(value)}`);
  }
  for (const expression of Array.isArray(selector.matchExpressions)
    ? selector.matchExpressions
    : []) {
    if (!expression || typeof expression !== "object") continue;
    const item = expression as { key?: string; operator?: string; values?: unknown[] };
    const values = Array.isArray(item.values) ? item.values.map(String).join(",") : "";
    parts.push(
      values
        ? `${item.key ?? "?"} ${item.operator ?? "?"} (${values})`
        : `${item.key ?? "?"} ${item.operator ?? "?"}`,
    );
  }
  return parts.length > 0 ? parts.join(", ") : "all workloads";
}

export function getCalicoPolicyNamespaceSelector(policy: any): string {
  const selector = policy?.spec?.namespaceSelector;
  return selector === undefined || selector === null || selector === ""
    ? "-"
    : String(selector);
}

export function getCalicoPolicyServiceAccountSelector(policy: any): string {
  const selector = policy?.spec?.serviceAccountSelector;
  return selector === undefined || selector === null || selector === ""
    ? "-"
    : String(selector);
}
