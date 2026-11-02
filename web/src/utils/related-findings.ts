import {
  gapReasonText,
  readActionFor,
  type RelatedFindingsGroup,
} from "@skyhook-io/k8s-ui";

export function relatedFindingLimitations(
  group: RelatedFindingsGroup,
): { kind: "unknown" | "truncated"; message: string }[] {
  const kinds =
    group.kinds.names
      ?.map((ref) => (ref.group ? `${ref.kind} (${ref.group})` : ref.kind))
      .join(", ") ?? `${group.kinds.count} resource kinds`;
  const limits: { kind: "unknown" | "truncated"; message: string }[] = [];
  if (group.unknown > 0) {
    limits.push({
      kind: "unknown",
      message: `${kinds}: ${group.unknown} of ${group.checked} checked resources could not be assessed.`,
    });
  }
  for (const gap of group.gaps) {
    const action = gap.action ?? readActionFor(gap.code);
    const next =
      action === "radar_config"
        ? " Check Radar's collection settings and cluster access."
        : action === "user_access"
          ? " Ask an administrator for read access."
          : action === "retry" || action === "wait"
            ? " Retry after the cache has loaded."
            : "";
    const reason =
      gap.code === "list_too_large"
        ? "too many resources to inspect"
        : gapReasonText(gap.code);
    const message = `${kinds}: incomplete related inventory — ${reason}.${next}`;
    if (!limits.some((limit) => limit.message === message))
      limits.push({ kind: "unknown", message });
  }
  if (group.truncated) {
    limits.push({
      kind: "truncated",
      message: "Additional related findings were omitted.",
    });
  }
  return limits;
}
