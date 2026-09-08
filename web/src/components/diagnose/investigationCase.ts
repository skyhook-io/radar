import { pluralToKind } from "@skyhook-io/k8s-ui";

import type {
  DiagnosisEvidenceItem,
  DiagnosisEvidenceRole,
  DiagnosisEvidenceSubject,
  DiagnosisRuledOut,
} from "../../api/diagnose";
import {
  investigationEvidenceSubjectRef,
  investigationSourceArgs,
  isInvestigationEvidenceRef,
  type InvestigationEvidenceObservation,
  type InvestigationEvidenceProjection,
  type InvestigationEvidenceSource,
} from "./investigationEvidence";
import { evidenceDisplaySnapshot } from "./investigationEvidencePresentation";

function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value !== "";
}

export type InvestigationCasePlacement = "card" | "revision" | "source";

/**
 * One linked item of the agent's case, resolved against the captured
 * evidence. `card` and `revision` items are pinned to exactly one observation;
 * a `source` item could not be pinned and renders only beside its source in
 * Assessment details.
 */
export interface InvestigationCaseItem {
  /** Position in Diagnosis.evidence; ruled-out entries point here. */
  index: number;
  role: DiagnosisEvidenceRole;
  claim: string;
  subject?: DiagnosisEvidenceSubject;
  source: InvestigationEvidenceSource;
  placement: InvestigationCasePlacement;
  groupId?: string;
  observation?: InvestigationEvidenceObservation;
}

export interface InvestigationCaseRuledOut {
  hypothesis: string;
  /** Always a card- or revision-placed item; unplaceable targets are not rendered. */
  item: InvestigationCaseItem;
}

export interface InvestigationCaseResolution {
  items: InvestigationCaseItem[];
  ruledOut: InvestigationCaseRuledOut[];
}

export const EVIDENCE_ROLES: ReadonlySet<string> =
  new Set<DiagnosisEvidenceRole>([
    "cause",
    "symptom",
    "context",
    "demoted",
    "rules_out",
  ]);

export function investigationCaseObservationKey(
  observation: Pick<InvestigationEvidenceObservation, "source" | "revision">,
): string {
  return `${observation.source.id}#${observation.revision}`;
}

/**
 * Resolves the agent's case for one assessment. Refs are re-validated against
 * the assessment turn exactly like root-cause refs; an item whose ref does not
 * resolve is dropped on its own. Placement follows the observation the
 * (ref, subject) pair names: exactly one match pins the claim to that
 * observation, anything else falls back to the source row.
 */
export function resolveInvestigationCase(
  projection: InvestigationEvidenceProjection,
  diagnosis:
    | { evidence?: DiagnosisEvidenceItem[]; ruledOut?: DiagnosisRuledOut[] }
    | null
    | undefined,
  assessmentTurnIndex: number,
): InvestigationCaseResolution {
  const evidence = diagnosis?.evidence ?? [];
  if (evidence.length === 0) return { items: [], ruledOut: [] };
  const byRef = new Map<string, InvestigationEvidenceSource[]>();
  for (const source of projection.evidenceRefSources) {
    if (source.turnIndex !== assessmentTurnIndex || !source.evidenceRef)
      continue;
    const matches = byRef.get(source.evidenceRef) ?? [];
    matches.push(source);
    byRef.set(source.evidenceRef, matches);
  }
  const citableSourceIds = new Set(
    projection.citableSources
      .filter((source) => source.turnIndex === assessmentTurnIndex)
      .map((source) => source.id),
  );
  const items: InvestigationCaseItem[] = [];
  const byIndex = new Map<number, InvestigationCaseItem>();
  evidence.forEach((entry, index) => {
    if (
      entry.status !== "linked" ||
      !entry.ref ||
      !isInvestigationEvidenceRef(entry.ref) ||
      !entry.role ||
      !EVIDENCE_ROLES.has(entry.role)
    ) {
      return;
    }
    const matches = byRef.get(entry.ref);
    if (matches?.length !== 1 || !citableSourceIds.has(matches[0].id)) return;
    const source = matches[0];
    const subject = validCaseSubject(entry.subject);
    const item: InvestigationCaseItem = {
      index,
      role: entry.role,
      claim: typeof entry.claim === "string" ? entry.claim.trim() : "",
      ...(subject ? { subject } : {}),
      source,
      placement: "source",
    };
    const candidates = projection.groups.flatMap((group) =>
      group.observations
        .filter(
          (observation) =>
            observation.source.id === source.id &&
            (!subject || observationMatchesSubject(observation, subject)),
        )
        .map((observation) => ({ group, observation })),
    );
    if (candidates.length === 1) {
      const { group, observation } = candidates[0];
      item.groupId = group.id;
      item.observation = observation;
      // An earlier read whose display content equals the card's latest is the
      // same fact; only a superseded, different observation gets its own row.
      item.placement =
        observation === group.latest ||
        evidenceDisplaySnapshot(observation) ===
          evidenceDisplaySnapshot(group.latest)
          ? "card"
          : "revision";
    }
    items.push(item);
    byIndex.set(index, item);
  });
  const ruledOut: InvestigationCaseRuledOut[] = [];
  for (const entry of diagnosis?.ruledOut ?? []) {
    if (
      typeof entry.hypothesis !== "string" ||
      !entry.hypothesis.trim() ||
      !Number.isInteger(entry.evidenceIndex)
    ) {
      continue;
    }
    const item = byIndex.get(entry.evidenceIndex);
    if (!item || item.placement === "source") continue;
    ruledOut.push({ hypothesis: entry.hypothesis.trim(), item });
  }
  return { items, ruledOut };
}

function validCaseSubject(
  value: DiagnosisEvidenceSubject | undefined,
): DiagnosisEvidenceSubject | undefined {
  if (!value || !nonEmptyString(value.kind) || !nonEmptyString(value.name))
    return undefined;
  const optional = (field: unknown) =>
    field === undefined || typeof field === "string";
  if (
    !optional(value.group) ||
    !optional(value.namespace) ||
    !optional(value.container) ||
    !optional(value.observation) ||
    (value.stream !== undefined &&
      value.stream !== "current" &&
      value.stream !== "previous")
  ) {
    return undefined;
  }
  return value;
}

interface ObservationSubjectIdentity {
  kind: string;
  group?: string;
  namespace?: string;
  name: string;
  container?: string;
  stream?: "current" | "previous";
}

/**
 * What an observation is about, for placing an agent claim. Observations that
 * state no resource of their own (events, changes without a subject, receipts)
 * inherit the resource their producing call was asked about, so a claim can
 * name "the events of Deployment api" inside a diagnose bundle.
 */
function observationSubjectIdentity(
  observation: InvestigationEvidenceObservation,
): ObservationSubjectIdentity | undefined {
  const { data } = observation;
  const stated = investigationEvidenceSubjectRef(data);
  if (stated) {
    return {
      ...stated,
      ...(data.type === "logs"
        ? {
            container: data.container,
            stream: data.previous ? "previous" : "current",
          }
        : {}),
    };
  }
  if (data.type === "changes" && data.subject?.kind) {
    return {
      kind: data.subject.kind,
      namespace: data.subject.namespace,
      name: data.subject.name,
    };
  }
  const args = investigationSourceArgs(observation.source);
  if (!args || !nonEmptyString(args.kind) || !nonEmptyString(args.name))
    return undefined;
  return {
    kind: args.kind,
    group: nonEmptyString(args.group) ? args.group : undefined,
    namespace: nonEmptyString(args.namespace) ? args.namespace : undefined,
    name: args.name,
  };
}

function sameKind(left: string, right: string): boolean {
  return pluralToKind(left).toLowerCase() === pluralToKind(right).toLowerCase();
}

/**
 * Every optional discriminator the agent omits is a wildcard; uniqueness of
 * the match, not completeness of the subject, is what places a claim.
 */
function observationMatchesSubject(
  observation: InvestigationEvidenceObservation,
  subject: DiagnosisEvidenceSubject,
): boolean {
  if (
    subject.observation !== undefined &&
    subject.observation.toLowerCase() !== observation.data.type
  ) {
    return false;
  }
  const identity = observationSubjectIdentity(observation);
  if (!identity) return false;
  if (!sameKind(identity.kind, subject.kind) || identity.name !== subject.name)
    return false;
  if (
    subject.group &&
    identity.group &&
    subject.group.toLowerCase() !== identity.group.toLowerCase()
  ) {
    return false;
  }
  if (
    subject.namespace &&
    identity.namespace &&
    subject.namespace !== identity.namespace
  ) {
    return false;
  }
  if (
    subject.container !== undefined &&
    identity.container !== subject.container
  )
    return false;
  if (subject.stream !== undefined && identity.stream !== subject.stream)
    return false;
  return true;
}
