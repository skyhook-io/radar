export type NetworkPolicyPeerType =
  | "pod"
  | "namespace"
  | "cidr"
  | "all"
  | "deny"
  | "combined";

export const NETWORK_POLICY_PEER_STYLES = {
  pod: "border-emerald-500/30 bg-emerald-500/8",
  namespace: "border-sky-500/30 bg-sky-500/8",
  cidr: "border-amber-500/30 bg-amber-500/8",
  combined: "border-emerald-500/30 bg-emerald-500/8",
  all: "border-theme-border bg-theme-elevated/50",
  deny: "border-red-500/30 bg-red-500/8",
} as const satisfies Record<NetworkPolicyPeerType, string>;

export const NETWORK_POLICY_PEER_DOTS = {
  pod: "bg-emerald-500",
  namespace: "bg-sky-500",
  cidr: "bg-amber-500",
  combined: "bg-emerald-500",
  all: "bg-theme-text-tertiary",
  deny: "bg-red-500",
} as const satisfies Record<NetworkPolicyPeerType, string>;
