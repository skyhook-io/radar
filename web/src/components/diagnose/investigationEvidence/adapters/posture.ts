import { nonEmptyString, record, stringArray } from "../parse";
import {
  invalidPayload,
  evidenceTierForRelevance,
  relevanceForResource,
  scopeFromArgs,
  type ProjectionBuilder,
} from "../observations";
import type {
  InvestigationEvidenceSource,
  InvestigationPostureFinding,
} from "../types";

// Posture findings are configuration checks. Only the ones about the
// investigated resource are evidence here; the rest of a cluster-wide scan
// stays in Activity, counted on the receipt.
function keep(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  origin: "audit" | "upgrade",
  findings: InvestigationPostureFinding[],
  relevances: string[],
  missing: string[],
  label: string,
): void {
  const kept = findings.filter((_, index) => relevances[index] !== "broader");
  const scope = scopeFromArgs(source);
  if (missing.length > 0) {
    builder.limit(
      source,
      label,
      `Inputs missing from the scan: ${missing.join(", ")}.`,
      "unknown",
    );
  }
  if (kept.length === 0) {
    if (!source.confirmedSuccess) return;
    builder.observe(`posture:${origin}:receipt:${scope}`, "receipt", source, {
      tier: evidenceTierForRelevance("checked", "producer-related"),
      relevance: "producer-related",
      tone: "neutral",
      title: `No ${label.toLowerCase()} on this resource`,
      summary: scope,
      data: {
        type: "receipt",
        checked: "posture",
        scope,
        message:
          [
            findings.length > 0
              ? `${findings.length} finding${findings.length === 1 ? "" : "s"} elsewhere in the scan.`
              : undefined,
            missing.length > 0
              ? `Not every check ran: inputs missing for ${missing.join(", ")}.`
              : undefined,
          ]
            .filter(Boolean)
            .join(" ") || undefined,
      },
    });
    return;
  }
  const onTarget = kept.filter((finding) => finding.target).length;
  const relevance = onTarget > 0 ? "target" : "producer-related";
  builder.observe(`posture:${origin}:${scope}`, "posture", source, {
    tier: evidenceTierForRelevance("context", relevance),
    relevance,
    tone: "neutral",
    title: label,
    summary: `${kept.length} finding${kept.length === 1 ? "" : "s"}${onTarget > 0 ? ` · ${onTarget} on this resource` : ""} · ${scope}`,
    data: { type: "posture", source: origin, findings: kept, scope },
  });
}

function relevanceOf(
  builder: ProjectionBuilder,
  ref: { kind: string; group?: string; namespace?: string; name: string },
): string {
  // An audit names its resource as Kind/namespace/name with no API group; the
  // investigation target's group stands in, as it does for events and changes.
  return relevanceForResource(builder, {
    kind: ref.kind,
    group: ref.group ?? builder.target.group,
    namespace: ref.namespace,
    name: ref.name,
  });
}

export function adaptClusterAudit(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  if (!value || !Array.isArray(value.findings)) {
    invalidPayload(builder, source);
    return;
  }
  const findings: InvestigationPostureFinding[] = [];
  const relevances: string[] = [];
  for (const entry of value.findings) {
    const item = record(entry);
    if (!item || !nonEmptyString(item.resource) || !nonEmptyString(item.check))
      continue;
    // "Deployment/default/web" or "Node//worker-1" for a cluster-scoped kind.
    const [kind, namespace, name] = item.resource.split("/");
    if (!kind || name === undefined) continue;
    const ref = { kind, namespace: namespace || undefined, name };
    const relevance = relevanceOf(builder, ref);
    findings.push({
      ...ref,
      check: item.check,
      severity: nonEmptyString(item.severity) ? item.severity : "medium",
      ...(nonEmptyString(item.category) ? { category: item.category } : {}),
      message: nonEmptyString(item.message) ? item.message : item.check,
      ...(nonEmptyString(item.remediation)
        ? { remediation: item.remediation }
        : {}),
      target: relevance === "target",
    });
    relevances.push(relevance);
  }
  if (value.truncated === true) {
    builder.limit(
      source,
      "Posture findings",
      "The scan hit its cap; findings past it were not returned. A namespace-scoped scan shows the rest.",
      "unknown",
    );
  }
  keep(
    builder,
    source,
    "audit",
    findings,
    relevances,
    stringArray(value.missingInputs) ?? [],
    "Posture findings",
  );
}

export function adaptUpgradeReadiness(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  if (!value) {
    invalidPayload(builder, source);
    return;
  }
  // The tier-1 result lists checks without findings; a check=<id> call
  // carries the findings under "check".
  const check = record(value.check);
  const rawFindings =
    check && Array.isArray(check.findings) ? check.findings : [];
  const target = nonEmptyString(value.targetVersion)
    ? value.targetVersion
    : undefined;
  const label = target ? `Upgrade findings for ${target}` : "Upgrade findings";
  if (!check) {
    if (!source.confirmedSuccess) return;
    const verdict = nonEmptyString(value.verdict)
      ? value.verdict.replaceAll("_", " ")
      : undefined;
    builder.observe(
      `posture:upgrade:verdict:${scopeFromArgs(source)}`,
      "receipt",
      source,
      {
        tier: evidenceTierForRelevance("checked", "broader"),
        relevance: "broader",
        tone: "neutral",
        title: label,
        summary: verdict ? `verdict ${verdict}` : scopeFromArgs(source),
        data: {
          type: "receipt",
          checked: "posture",
          scope: scopeFromArgs(source),
          message: nonEmptyString(value.verdictCaveat)
            ? value.verdictCaveat
            : undefined,
        },
      },
    );
    return;
  }
  const findings: InvestigationPostureFinding[] = [];
  const relevances: string[] = [];
  for (const entry of rawFindings) {
    const item = record(entry);
    const resource = record(item?.resource);
    const managedBy = record(item?.managedBy);
    const named = resource ?? managedBy;
    if (
      !item ||
      !named ||
      !nonEmptyString(named.kind) ||
      !nonEmptyString(named.name)
    )
      continue;
    const refOf = (value: Record<string, unknown>) => ({
      kind: String(value.kind),
      group: nonEmptyString(value.group) ? value.group : undefined,
      namespace: nonEmptyString(value.namespace) ? value.namespace : undefined,
      name: String(value.name),
    });
    const ref = refOf(named);
    // A finding on an object the workload owns (its HPA, its Ingress) is a
    // finding about the workload, and the workload's name reaches it.
    const owner =
      resource &&
      managedBy &&
      nonEmptyString(managedBy.kind) &&
      nonEmptyString(managedBy.name)
        ? refOf(managedBy)
        : undefined;
    let relevance = relevanceOf(builder, ref);
    if (relevance === "broader" && owner) {
      relevance = relevanceOf(builder, owner);
    }
    const evidence = record(item.evidence);
    findings.push({
      ...ref,
      check: nonEmptyString(item.title)
        ? item.title
        : nonEmptyString(check.title)
          ? check.title
          : "Upgrade check",
      severity: nonEmptyString(item.level) ? item.level : "review",
      category: nonEmptyString(check.category) ? check.category : undefined,
      message: [
        nonEmptyString(item.impact) ? item.impact : undefined,
        evidence && nonEmptyString(evidence.detail)
          ? evidence.detail
          : undefined,
      ]
        .filter(Boolean)
        .join(" "),
      ...(nonEmptyString(item.remediation)
        ? { remediation: item.remediation }
        : {}),
      target: relevance === "target",
      ...(owner ? { managedBy: owner } : {}),
    });
    relevances.push(relevance);
  }
  if (
    typeof check.findingsTruncated === "number" &&
    check.findingsTruncated > 0
  ) {
    builder.limit(
      source,
      label,
      `${check.findingsTruncated} more finding${check.findingsTruncated === 1 ? "" : "s"} past this page were not returned; the next page has them.`,
      "unknown",
    );
  }
  keep(builder, source, "upgrade", findings, relevances, [], label);
}
