import { displayKind } from "@skyhook-io/k8s-ui";
import { apiVersionToGroup } from "../../../../utils/navigation";
import {
  ProjectionBuilder,
  evidenceTierForRelevance,
  invalidPayload,
} from "../observations";
import {
  nonEmptyString,
  nonNegativeInteger,
  record,
  stringArray,
} from "../parse";
import {
  type InvestigationAccessCheck,
  type InvestigationEvidenceObservation,
  type InvestigationEvidenceRelevance,
  type InvestigationEvidenceSource,
  type InvestigationKubernetesResource,
  type InvestigationPermissionBinding,
  type InvestigationPermissionSubject,
} from "../types";

function permissionSubject(
  value: unknown,
): InvestigationPermissionSubject | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.kind) ||
    !nonEmptyString(candidate.name) ||
    (candidate.namespace !== undefined &&
      typeof candidate.namespace !== "string")
  ) {
    return undefined;
  }
  return {
    kind: candidate.kind,
    name: candidate.name,
    ...(nonEmptyString(candidate.namespace)
      ? { namespace: candidate.namespace }
      : {}),
  };
}
export function accessCheck(
  value: unknown,
): InvestigationAccessCheck | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.verb) ||
    !nonEmptyString(candidate.resource) ||
    typeof candidate.namespace !== "string" ||
    typeof candidate.allowed !== "boolean" ||
    (candidate.denied !== undefined && typeof candidate.denied !== "boolean")
  ) {
    return undefined;
  }
  for (const field of [
    "group",
    "subresource",
    "resourceName",
    "reason",
    "evaluationError",
  ] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string")
      return undefined;
  }
  return {
    ...(candidate as unknown as InvestigationAccessCheck),
    denied: candidate.denied === true,
  };
}
function permissionBinding(
  value: unknown,
): InvestigationPermissionBinding | undefined {
  const candidate = record(value);
  if (
    !candidate ||
    !nonEmptyString(candidate.bindingKind) ||
    !nonEmptyString(candidate.bindingName) ||
    !nonEmptyString(candidate.roleKind) ||
    !nonEmptyString(candidate.roleName) ||
    !nonNegativeInteger(candidate.rulesCount)
  ) {
    return undefined;
  }
  for (const field of [
    "bindingNamespace",
    "roleNamespace",
    "inheritedFromGroup",
  ] as const) {
    if (candidate[field] !== undefined && typeof candidate[field] !== "string")
      return undefined;
  }
  return candidate as unknown as InvestigationPermissionBinding;
}
// Kinds whose spec carries a PodSpec, by API group. Anything else with a
// `spec.template.spec` (an ApplicationSet, for one) templates something that
// is not a Pod and runs as no account at all.
const POD_TEMPLATE_KINDS: Readonly<Record<string, readonly string[]>> = {
  "": ["Pod"],
  apps: ["Deployment", "StatefulSet", "DaemonSet", "ReplicaSet"],
  batch: ["Job", "CronJob"],
  "argoproj.io": ["Rollout"],
};
function podSpecServiceAccount(
  resource: InvestigationKubernetesResource,
): string | undefined {
  const group = apiVersionToGroup(resource.apiVersion);
  if (!POD_TEMPLATE_KINDS[group]?.includes(resource.kind)) return undefined;
  const spec = record(resource.spec);
  const podSpec =
    resource.kind === "Pod"
      ? spec
      : resource.kind === "CronJob"
        ? record(
            record(record(record(spec?.jobTemplate)?.spec)?.template)?.spec,
          )
        : record(record(spec?.template)?.spec);
  if (!podSpec) return undefined;
  // An unset serviceAccountName runs as the namespace's `default` account.
  return nonEmptyString(podSpec.serviceAccountName)
    ? podSpec.serviceAccountName
    : "default";
}
/**
 * The ServiceAccount the target runs as, read from a resource observation of
 * the target in the same turn. Only the captured resource can state this; the
 * permission subject is never assumed related by name alone.
 */
function targetServiceAccountName(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
): string | undefined {
  let newest: InvestigationEvidenceObservation | undefined;
  for (const group of builder.groups) {
    if (group.kind !== "resource") continue;
    for (const observation of group.observations) {
      if (
        observation.source.turnIndex !== source.turnIndex ||
        observation.relevance !== "target" ||
        observation.data.type !== "resource" ||
        (newest && observation.source.order < newest.source.order)
      ) {
        continue;
      }
      newest = observation;
    }
  }
  if (!newest || newest.data.type !== "resource") return undefined;
  return (
    newest.data.resourceContext?.uses?.serviceAccount?.name ??
    podSpecServiceAccount(newest.data.resource)
  );
}
export function adaptSubjectPermissions(
  builder: ProjectionBuilder,
  source: InvestigationEvidenceSource,
  payload: unknown,
): void {
  const value = record(payload);
  const subject = value ? permissionSubject(value.subject) : undefined;
  if (!value || !subject) {
    invalidPayload(builder, source);
    return;
  }
  // Only a captured resource can tie a principal to the target. Any other
  // subject the agent chose to check is context, however adverse the answer.
  const relevance: InvestigationEvidenceRelevance =
    subject.kind === "ServiceAccount" &&
    subject.namespace !== undefined &&
    subject.namespace === builder.target.namespace &&
    targetServiceAccountName(builder, source) === subject.name
      ? "target"
      : "broader";
  const subjectLabel = `${displayKind(subject.kind)} ${subject.namespace ? `${subject.namespace}/` : ""}${subject.name}`;
  const subjectKey = `${subject.kind}:${subject.namespace ?? ""}:${subject.name}`;

  if (value.accessCheck !== undefined) {
    const check = accessCheck(value.accessCheck);
    if (!check) {
      invalidPayload(builder, source, "Access check");
      return;
    }
    const resourceLabel = `${check.resource}${check.subresource ? `/${check.subresource}` : ""}${check.group ? `.${check.group}` : ""}`;
    // An authorizer that could not decide has established neither answer. The
    // evaluation error already reaches the coverage strip below, but a card
    // reading "cannot verb resource" is the part an operator acts on, and a
    // webhook returning partial data is not a denial.
    // Only when the authorizer decided nothing. Kubernetes returns this error
    // alongside a real verdict too — one webhook failing while another allows —
    // and rewriting a decided allow or deny as "could not check" loses the
    // answer and puts an allow in the warning list.
    const unresolved =
      nonEmptyString(check.evaluationError) && !check.allowed && !check.denied;
    const denied = !check.allowed && !unresolved;
    const verdict = unresolved
      ? `Could not be evaluated: ${check.evaluationError}`
      : check.reason ||
        (check.allowed
          ? "Allowed by RBAC"
          : check.denied
            ? "Explicitly denied"
            : "No RBAC rule allows it");
    builder.observe(
      `permissions:check:${subjectKey}:${check.verb}:${check.group ?? ""}:${check.resource}:${check.subresource ?? ""}:${check.namespace}:${check.resourceName ?? ""}`,
      "permissions",
      source,
      {
        tier: evidenceTierForRelevance(
          denied || unresolved ? "supporting" : "context",
          relevance,
        ),
        relevance,
        tone: denied || unresolved ? "warning" : "info",
        title: unresolved
          ? `Could not check whether ${subjectLabel} can ${check.verb} ${resourceLabel}`
          : `${subjectLabel} ${denied ? "cannot" : "can"} ${check.verb} ${resourceLabel}`,
        summary: `${verdict} · ${check.namespace ? `namespace ${check.namespace}` : "cluster-wide"}${check.resourceName ? ` · ${check.resourceName}` : ""}`,
        data: { type: "permissions", subject, accessCheck: check },
      },
    );
    if (nonEmptyString(check.evaluationError)) {
      builder.limit(source, "Permissions", check.evaluationError, "error");
    }
    return;
  }

  const bindingsRaw = value.bindings;
  if (!Array.isArray(bindingsRaw)) {
    invalidPayload(builder, source);
    return;
  }
  const bindings = bindingsRaw
    .map(permissionBinding)
    .filter((item): item is InvestigationPermissionBinding => Boolean(item));
  if (
    bindings.length !== bindingsRaw.length ||
    !Array.isArray(value.flatRules) ||
    !value.flatRules.every((rule) => stringArray(record(rule)?.verbs)) ||
    (value.truncated !== undefined && typeof value.truncated !== "boolean") ||
    (value.podsTotal !== undefined && !nonNegativeInteger(value.podsTotal)) ||
    (value.usedByPods !== undefined && !stringArray(value.usedByPods))
  ) {
    invalidPayload(builder, source);
    return;
  }
  const truncated = value.truncated === true;
  const flatRulesCount = value.flatRules.length;
  const usedByPods = stringArray(value.usedByPods) ?? [];
  builder.observe(`permissions:subject:${subjectKey}`, "permissions", source, {
    tier: evidenceTierForRelevance("context", relevance),
    relevance,
    tone: "info",
    title: `Permissions of ${subjectLabel}`,
    summary: `${bindings.length} binding${bindings.length === 1 ? "" : "s"} · ${flatRulesCount}${truncated ? "+" : ""} effective rule${flatRulesCount === 1 && !truncated ? "" : "s"}`,
    data: {
      type: "permissions",
      subject,
      bindings,
      flatRulesCount,
      truncated,
      usedByPods,
      podsTotal: value.podsTotal as number | undefined,
    },
  });
  if (truncated) {
    builder.limit(
      source,
      "Permissions",
      nonEmptyString(value.narrowHint)
        ? value.narrowHint
        : "The effective rule list was truncated; it is not the subject's complete permission set.",
      "truncated",
    );
  }
  if (nonEmptyString(value.subjectWarning)) {
    builder.limit(source, "Permissions", value.subjectWarning, "unknown");
  }
  if (
    typeof value.podsTotal === "number" &&
    value.podsTotal > usedByPods.length
  ) {
    builder.limit(
      source,
      "Permissions",
      `Only ${usedByPods.length} of ${value.podsTotal} pods running as this subject were listed.`,
      "truncated",
    );
  }
}
