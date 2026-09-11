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
  type InvestigationEvidenceGroup,
  type InvestigationEvidenceObservation,
  type InvestigationEvidenceProjection,
  type InvestigationEvidenceSource,
} from "./investigationEvidence";

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

/**
 * Notes the agent put on cards in earlier assessments stay on those cards
 * when a later turn takes over the pane's case, unless the later turn
 * addressed the same card; a follow-up about one chart must not strip the
 * initial assessment's reading from everything else. Ordering and the
 * ruled-out list follow the live turn alone, and source-placed notes stay
 * listed under their own assessment. `earlier` is newest first.
 */
export function mergeInvestigationCases(
  live: InvestigationCaseResolution | undefined,
  earlier: readonly (InvestigationCaseResolution | undefined)[],
): InvestigationCaseResolution | undefined {
  const liveItems = live?.items ?? [];
  const covered = new Set(
    liveItems.flatMap((item) => (item.groupId ? [item.groupId] : [])),
  );
  const carried: InvestigationCaseItem[] = [];
  for (const resolution of earlier) {
    // The newest assessment that spoke about a group wins it outright, and it
    // wins with everything it said: a card note and a pinned revision note are
    // two readings of one group, not rivals. Coverage is therefore taken after
    // the whole resolution, not as each of its items is carried.
    const takenHere = new Set<string>();
    for (const item of resolution?.items ?? []) {
      if (item.placement === "source" || !item.groupId) continue;
      if (covered.has(item.groupId)) continue;
      takenHere.add(item.groupId);
      carried.push(item);
    }
    for (const groupId of takenHere) covered.add(groupId);
  }
  if (carried.length === 0) return live;
  return { items: [...liveItems, ...carried], ruledOut: live?.ruledOut ?? [] };
}

/**
 * The subset of one assessment's items that survived into the case the pane
 * actually renders. Matching is by value, not object identity: the merge is
 * fed freshly resolved copies of every turn, so an identity test would report
 * that an assessment's own note had vanished the moment any other turn was
 * re-resolved.
 */
export function investigationCaseItemsStillRendered(
  assessmentItems: readonly InvestigationCaseItem[] | undefined,
  renderedItems: readonly InvestigationCaseItem[] | undefined,
): InvestigationCaseItem[] {
  if (!assessmentItems?.length || !renderedItems?.length) return [];
  const key = (item: InvestigationCaseItem) =>
    [
      item.index,
      item.source.id,
      item.placement,
      item.groupId ?? "",
      item.observation ? investigationCaseObservationKey(item.observation) : "",
    ].join("\u0000");
  const rendered = new Set(renderedItems.map(key));
  return assessmentItems.filter((item) => rendered.has(key(item)));
}

/**
 * One half of a Go↔TS contract: `evidenceRoles` in internal/ai/parse.go and the
 * DiagnosisEvidenceRole union in api/diagnose.ts must list exactly these roles.
 * A role the parser accepts but this set omits is bound server-side and then
 * silently dropped here. Change all three together.
 */
export const EVIDENCE_ROLES: ReadonlySet<string> =
  new Set<DiagnosisEvidenceRole>([
    "cause",
    "symptom",
    "context",
    "benign",
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
    // A subject the agent supplied but got wrong is not the same as one it
    // deliberately omitted. Omission asks Radar to place the note anywhere the
    // cited call produced; a malformed subject asked for something specific
    // that does not resolve, so the note stays beside its source rather than
    // landing on whichever observation happens to be the only one.
    const subjectUnusable = entry.subject !== undefined && !subject;
    const item: InvestigationCaseItem = {
      index,
      role: entry.role,
      claim: typeof entry.claim === "string" ? entry.claim.trim() : "",
      ...(subject ? { subject } : {}),
      source,
      placement: "source",
    };
    const candidates = subjectUnusable
      ? []
      : projection.groups.flatMap((group) =>
          group.observations
            .filter(
              (observation) =>
                observation.source.id === source.id &&
                (!subject ||
                  observationMatchesSubject(group, observation, subject)),
            )
            .map((observation) => ({ group, observation })),
        );
    if (candidates.length === 1) {
      const { group, observation } = candidates[0];
      item.groupId = group.id;
      item.observation = observation;
      // The claim stays bound to the exact observation the agent cited. Only
      // the card's authoritative observation carries it on the card head; a
      // superseded read keeps its own revision row even when it displays the
      // same, because display equivalence ignores counts and times.
      item.placement = observation === group.latest ? "card" : "revision";
    }
    items.push(item);
    byIndex.set(index, item);
  });
  const ruledOut: InvestigationCaseRuledOut[] = [];
  const seenRuledOut = new Set<string>();
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
    const hypothesis = entry.hypothesis.trim();
    const dedupeKey = `${entry.evidenceIndex}\u0000${hypothesis}`;
    if (seenRuledOut.has(dedupeKey)) continue;
    seenRuledOut.add(dedupeKey);
    ruledOut.push({ hypothesis, item });
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
  /** Empty string is a known core group; undefined means the producer did not say. */
  group?: string;
  /** Empty string is known cluster scope; undefined means the producer did not say. */
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
 *
 * A stated resource identity knows its API group and scope: a core resource
 * has group "" and a cluster-scoped one has namespace "". Only args-derived
 * identities leave those undefined, and only then do they act as wildcards.
 */
function observationSubjectIdentity(
  observation: InvestigationEvidenceObservation,
): ObservationSubjectIdentity | undefined {
  const { data } = observation;
  const stated = investigationEvidenceSubjectRef(data);
  if (stated) {
    // Producers that state a resource from its own object, or from the
    // investigation target, know the group exactly; a subject ref copied from
    // another producer's payload may not.
    const groupKnown =
      data.type === "resource" ||
      data.type === "issue" ||
      data.type === "logs" ||
      data.type === "crash" ||
      data.type === "startup" ||
      data.type === "helm" ||
      data.type === "permissions" ||
      data.type === "metrics";
    return {
      kind: stated.kind,
      group: stated.group ?? (groupKnown ? "" : undefined),
      namespace: stated.namespace ?? "",
      name: stated.name,
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
 * A discriminator the agent omits is a wildcard, and so is one the producer
 * did not state; every discriminator both sides supply must match. Uniqueness
 * of the match, not completeness of the subject, is what places a claim.
 */
function observationMatchesSubject(
  group: InvestigationEvidenceGroup,
  observation: InvestigationEvidenceObservation,
  subject: DiagnosisEvidenceSubject,
): boolean {
  if (subject.observation !== undefined) {
    // A diagnose bundle captures several vitals charts for one resource, so
    // "metrics" alone cannot name one; "metrics:<category>" picks the chart
    // whose identity ends in that category. An agent-run query is one chart,
    // so a qualifier on it carries no meaning.
    const [kind, qualifier] = subject.observation.toLowerCase().split(":", 2);
    if (kind !== observation.data.type) return false;
    if (
      qualifier !== undefined &&
      observation.data.type === "metrics" &&
      observation.data.origin === "diagnose" &&
      !group.identity.endsWith(`:${qualifier}`)
    ) {
      return false;
    }
  }
  const identities = observationIdentities(observation);
  // An observation that states no resource of its own (an agent-run query
  // whose selectors do not name the target exactly, a topology summary) can
  // only be named by its evidence kind; the ref and the uniqueness rule do
  // the rest.
  if (identities.length === 0) return subject.observation !== undefined;
  return identities.some((identity) =>
    identityMatchesSubject(identity, observation, subject),
  );
}

/**
 * A log stream is identified by its pod, and also by the workload its
 * producing call was asked about: an agent naming "the Deployment's
 * container logs" means the stream a diagnose bundle read for that
 * Deployment, and uniqueness still decides whether that names one.
 */
function observationIdentities(
  observation: InvestigationEvidenceObservation,
): ObservationSubjectIdentity[] {
  const stated = observationSubjectIdentity(observation);
  const identities = stated ? [stated] : [];
  if (observation.data.type === "logs" && stated) {
    const args = investigationSourceArgs(observation.source);
    if (args && nonEmptyString(args.kind) && nonEmptyString(args.name)) {
      identities.push({
        kind: args.kind,
        group: nonEmptyString(args.group) ? args.group : undefined,
        namespace: nonEmptyString(args.namespace) ? args.namespace : undefined,
        name: args.name,
        container: stated.container,
        stream: stated.stream,
      });
    }
  }
  return identities;
}

function identityMatchesSubject(
  identity: ObservationSubjectIdentity,
  observation: InvestigationEvidenceObservation,
  subject: DiagnosisEvidenceSubject,
): boolean {
  if (!sameKind(identity.kind, subject.kind) || identity.name !== subject.name)
    return false;
  if (
    subject.group !== undefined &&
    identity.group !== undefined &&
    subject.group.toLowerCase() !== identity.group.toLowerCase()
  ) {
    return false;
  }
  if (
    subject.namespace !== undefined &&
    identity.namespace !== undefined &&
    subject.namespace !== identity.namespace
  ) {
    return false;
  }
  // Container and stream are log-stream dimensions. Without an observation
  // kind they say "a log stream"; with one stated for another kind (a
  // container-scoped metrics query, say) they are descriptive only.
  const streamDimensions =
    observation.data.type === "logs" || subject.observation === undefined;
  if (
    streamDimensions &&
    subject.container !== undefined &&
    identity.container !== subject.container
  )
    return false;
  if (
    streamDimensions &&
    subject.stream !== undefined &&
    identity.stream !== subject.stream
  )
    return false;
  return true;
}
