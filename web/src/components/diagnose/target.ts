import { resourceKey } from "@skyhook-io/k8s-ui";

import { kindToPluralWithGroup } from "../../utils/navigation";

export interface InvestigationTargetIdentity {
  kind: string;
  /** Kubernetes API group; empty means core. */
  group: string;
  namespace: string;
  name: string;
}

// Normalize the Kind/resource-name boundary before comparing a run returned by
// the API (singular Kind) with a UI action (often a plural resource name).
export function runTargetKey(
  kind: string,
  namespace: string,
  name: string,
  group: string,
): string {
  return resourceKey(
    group,
    kindToPluralWithGroup(kind, group),
    namespace,
    name,
  );
}

// Keep the UI aligned with the CLI and the prompts: a non-core target is shown
// as Kind.api.group, which is the Kubernetes-qualified identity users can copy.
export function formatInvestigationTarget(
  target: InvestigationTargetIdentity,
): string {
  const kind = target.group ? `${target.kind}.${target.group}` : target.kind;
  return `${kind} ${target.namespace ? `${target.namespace}/` : ""}${target.name}`;
}
