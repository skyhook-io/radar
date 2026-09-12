import { CORE_RESOURCES } from "@skyhook-io/k8s-ui";
import { fnv1a32 } from "@skyhook-io/k8s-ui/utils/structure-hash";
import { apiVersionToGroup } from "../../../utils/navigation";
import { type DiagnosisResourceRef } from "../diagnoseEvidenceTypes";
import { type RootCauseEvidence } from "../../../api/diagnose";
import { nonEmptyString, parseJSON, record } from "./parse";
import {
  type InvestigationEvidenceData,
  type InvestigationEvidenceProjection,
  type InvestigationEvidenceSource,
  type InvestigationRootCauseEvidenceLink,
  type InvestigationRootCauseEvidenceResolution,
} from "./types";

function exactDiagnosisResourceRef(ref: {
  kind?: unknown;
  group?: unknown;
  namespace?: unknown;
  name?: unknown;
}): DiagnosisResourceRef | undefined {
  const { kind, name } = ref;
  if (
    !nonEmptyString(kind) ||
    kind.trim() !== kind ||
    !nonEmptyString(name) ||
    name.trim() !== name ||
    (ref.group !== undefined &&
      (typeof ref.group !== "string" || ref.group.trim() !== ref.group)) ||
    (ref.namespace !== undefined &&
      (typeof ref.namespace !== "string" ||
        ref.namespace.trim() !== ref.namespace))
  ) {
    return undefined;
  }
  const group = ref.group || undefined;
  const namespace = ref.namespace || undefined;
  const knownKinds = CORE_RESOURCES.filter(
    (resource) =>
      resource.kind.toLowerCase() === kind.toLowerCase() &&
      (group === undefined || resource.group === group),
  );
  const knownScopes = new Set(
    knownKinds.map((resource) => resource.namespaced),
  );
  // This is deliberately only a negative guard. Unknown kinds/groups may be
  // cluster-scoped CRDs, so suppress the link only when Radar's existing
  // resource metadata identifies the kind's scope without ambiguity.
  if (knownScopes.size === 1) {
    if (knownScopes.has(true) && !namespace) return undefined;
    if (knownScopes.has(false) && namespace) return undefined;
  }
  return {
    kind,
    name,
    ...(group ? { group } : {}),
    ...(namespace ? { namespace } : {}),
  };
}
/**
 * The Kubernetes resource an evidence item is about, when the producer payload
 * states one unambiguously. Pod-shaped evidence resolves only through the
 * namespace its producing check actually read; the investigation target's
 * namespace is deliberately never borrowed.
 */
export function investigationEvidenceSubjectRef(
  data: InvestigationEvidenceData,
): DiagnosisResourceRef | undefined {
  const ref = ((): DiagnosisResourceRef | undefined => {
    switch (data.type) {
      case "issue":
        return {
          kind: data.issue.kind,
          group: data.issue.group,
          namespace: data.issue.namespace,
          name: data.issue.name,
        };
      case "startup":
        return data.subject;
      case "resource": {
        const apiVersion = data.resource.apiVersion;
        const group = apiVersionToGroup(apiVersion);
        return {
          kind: data.resource.kind,
          group: group || undefined,
          namespace: data.resource.metadata.namespace,
          name: data.resource.metadata.name,
        };
      }
      case "logs":
        if (!data.namespace) return undefined;
        return { kind: "Pod", namespace: data.namespace, name: data.pod };
      case "crash":
        if (data.crash.pods.length !== 1 || !data.namespace) return undefined;
        return {
          kind: "Pod",
          namespace: data.namespace,
          name: data.crash.pods[0],
        };
      case "network":
        return data.network.subject;
      case "relationships":
        return data.root;
      case "helm":
        // A resource ref cannot carry the storage namespace, and opening the
        // release by name alone would read the wrong storage.
        if (
          data.release.storageNamespace !== undefined &&
          data.release.storageNamespace !== data.release.namespace
        ) {
          return undefined;
        }
        return {
          kind: "HelmRelease",
          group: "helm.sh",
          namespace: data.release.namespace,
          name: data.release.name,
        };
      case "permissions":
        // Users and Groups are principals, not Kubernetes objects.
        if (data.subject.kind !== "ServiceAccount") return undefined;
        return {
          kind: "ServiceAccount",
          namespace: data.subject.namespace,
          name: data.subject.name,
        };
      case "metrics":
        return data.subject;
      default:
        return undefined;
    }
  })();
  return ref ? exactDiagnosisResourceRef(ref) : undefined;
}
function domToken(value: string): string {
  // Underscore is reserved as the escape delimiter, so raw input can never
  // impersonate an encoded code point ("/" vs. the literal "_x2f_"). The
  // empty sentinel is safe for the same reason: a literal "_empty_" is escaped.
  if (!value) return "_empty_";
  return Array.from(value, (character) =>
    /[A-Za-z0-9-]/.test(character)
      ? character
      : `_x${character.codePointAt(0)!.toString(16)}_`,
  ).join("");
}
export function investigationEvidenceSourceId(
  turnIndex: number,
  stepId: string,
): string {
  return `turn-${turnIndex}-step-${domToken(stepId)}`;
}
export function investigationActivitySourceDomId(sourceId: string): string {
  return `investigation-activity-${sourceId}`;
}
export function investigationEvidenceSourceDomId(sourceId: string): string {
  return `investigation-evidence-${sourceId}`;
}
export const investigationEvidenceRefRe =
  /^ev_[a-z2-7]{26,128}_[a-z2-7]{26,128}$/;
export function isInvestigationEvidenceRef(value: string): boolean {
  return investigationEvidenceRefRe.test(value);
}
export function investigationSourceArgs(
  source: InvestigationEvidenceSource,
): Record<string, unknown> | undefined {
  return record(source.args ? parseJSON(source.args) : undefined);
}
export function resolveInvestigationRootCauseEvidence(
  projection: InvestigationEvidenceProjection,
  evidence: RootCauseEvidence | undefined,
  assessmentTurnIndex: number,
): InvestigationRootCauseEvidenceResolution {
  if (!evidence || evidence.status === "missing") {
    return { status: "missing", links: [] };
  }
  if (evidence.status !== "linked") {
    return { status: "invalid", links: [] };
  }
  const refs = evidence.refs;
  if (
    !refs ||
    refs.length < 1 ||
    refs.length > 3 ||
    new Set(refs).size !== refs.length ||
    refs.some((ref) => !investigationEvidenceRefRe.test(ref)) ||
    refs.some((ref) => ref.split("_")[1] !== refs[0].split("_")[1])
  ) {
    return { status: "invalid", links: [] };
  }

  const byRef = new Map<string, InvestigationEvidenceSource[]>();
  for (const source of projection.evidenceRefSources) {
    if (source.turnIndex !== assessmentTurnIndex) continue;
    if (!source.evidenceRef) continue;
    const matches = byRef.get(source.evidenceRef) ?? [];
    matches.push(source);
    byRef.set(source.evidenceRef, matches);
  }
  const citableSourceIds = new Set(
    projection.citableSources
      .filter((source) => source.turnIndex === assessmentTurnIndex)
      .map((source) => source.id),
  );
  const links: InvestigationRootCauseEvidenceLink[] = [];
  for (const ref of refs) {
    const matches = byRef.get(ref);
    // Match the server's fail-closed binding: every current-turn occurrence
    // counts before success/completeness eligibility is considered.
    if (matches?.length !== 1 || !citableSourceIds.has(matches[0].id)) {
      return { status: "invalid", links: [] };
    }
    const source = matches[0];
    const originalGroup = source.primaryGroupId
      ? projection.groups.find((group) => group.id === source.primaryGroupId)
      : undefined;
    links.push({
      source,
      originalGroupId: originalGroup?.observations.some(
        (observation) => observation.source.id === source.id,
      )
        ? originalGroup.id
        : undefined,
    });
  }
  return { status: "linked", links };
}
export function investigationEvidenceStepIdsByTurn(
  projection: InvestigationEvidenceProjection,
  visibleGroupIds?: ReadonlySet<string>,
): Map<number, Set<string>> {
  const byTurn = new Map<number, Set<string>>();
  const linkedSourceIds = new Set<string>();
  for (const group of projection.groups) {
    if (visibleGroupIds && !visibleGroupIds.has(group.id)) continue;
    for (const observation of group.observations) {
      if (visibleGroupIds && observation.source.primaryGroupId !== group.id)
        continue;
      linkedSourceIds.add(observation.source.id);
    }
  }
  for (const limitation of projection.limitations) {
    for (const source of limitation.sources) linkedSourceIds.add(source.id);
  }
  const navigableSources = new Map(
    [...projection.sources, ...projection.citableSources].map((source) => [
      source.id,
      source,
    ]),
  );
  for (const source of navigableSources.values()) {
    if (!linkedSourceIds.has(source.id)) continue;
    const stepIds = byTurn.get(source.turnIndex) ?? new Set<string>();
    stepIds.add(source.stepId);
    byTurn.set(source.turnIndex, stepIds);
  }
  return byTurn;
}
export function stableHash(value: string): string {
  // FNV-1a keeps long resource/log identities out of DOM IDs. The raw identity
  // remains the Map key, so a hash collision can never merge evidence.
  return fnv1a32(value).toString(36);
}
