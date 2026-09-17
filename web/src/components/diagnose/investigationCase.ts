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
  /** What the agent says this result does not cover. */
  gap?: string;
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
 * the run up to and including the assessment turn, exactly like root-cause
 * refs — a revised assessment may rest on a result read in an earlier turn,
 * and the observation it resolves to is that earlier read, never a later one.
 * An item whose ref does not resolve is dropped on its own. Placement follows
 * the observation the (ref, subject) pair names: exactly one match pins the
 * claim to that observation, anything else falls back to the source row.
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
  const targetPods = new Set(projection.targetPods);
  const byRef = new Map<string, InvestigationEvidenceSource[]>();
  for (const source of projection.evidenceRefSources) {
    if (source.turnIndex > assessmentTurnIndex || !source.evidenceRef) continue;
    const matches = byRef.get(source.evidenceRef) ?? [];
    matches.push(source);
    byRef.set(source.evidenceRef, matches);
  }
  const citableSourceIds = new Set(
    projection.citableSources
      .filter((source) => source.turnIndex <= assessmentTurnIndex)
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
      ...(typeof entry.gap === "string" && entry.gap.trim()
        ? { gap: entry.gap.trim() }
        : {}),
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
                  observationMatchesSubject(
                    group,
                    observation,
                    subject,
                    targetPods,
                  )),
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
  if (!value || !nonEmptyString(value.kind)) return undefined;
  const optional = (field: unknown) =>
    field === undefined || typeof field === "string";
  if (
    !optional(value.name) ||
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
  /** A listing names a kind (and scope) but no single object. */
  listing?: boolean;
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
  // A listing whose call names no kind (list_namespaces) is still a listing
  // of the one kind its rows carry.
  if (data.type === "inventory" && !nonEmptyString(args?.kind)) {
    const kinds = new Set(data.resources.map((resource) => resource.kind));
    if (kinds.size !== 1) return undefined;
    return {
      kind: [...kinds][0],
      namespace: nonEmptyString(args?.namespace) ? args.namespace : undefined,
      name: "",
      listing: true,
    };
  }
  if (!args || !nonEmptyString(args.kind)) return undefined;
  return {
    kind: args.kind,
    group: nonEmptyString(args.group) ? args.group : undefined,
    namespace: nonEmptyString(args.namespace) ? args.namespace : undefined,
    name: nonEmptyString(args.name) ? args.name : "",
    ...(nonEmptyString(args.name) ? {} : { listing: true }),
  };
}

// No CRD lives in a built-in API group: the core group under its three
// spellings, the four legacy unsuffixed groups, or any group under k8s.io.
const LEGACY_BUILT_IN_GROUPS = new Set([
  "",
  "core",
  "v1",
  "apps",
  "batch",
  "autoscaling",
  "policy",
]);
function isBuiltInGroup(group: string): boolean {
  return LEGACY_BUILT_IN_GROUPS.has(group) || group.endsWith(".k8s.io");
}

export function sameKind(left: string, right: string): boolean {
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
  targetPods: ReadonlySet<string>,
): boolean {
  if (subject.observation !== undefined) {
    // A diagnose bundle captures several vitals charts for one resource, so
    // "metrics" alone cannot name one; "metrics:<category>" picks the chart
    // whose identity ends in that category. An agent-run query is one chart,
    // so a qualifier on it carries no meaning.
    const [kind, qualifier] = subject.observation.toLowerCase().split(":", 2);
    // A listing is resources too: "resource" names an inventory card as well.
    const inventoryAsResource =
      kind === "resource" && observation.data.type === "inventory";
    if (kind !== observation.data.type && !inventoryAsResource) return false;
    if (
      qualifier !== undefined &&
      observation.data.type === "metrics" &&
      observation.data.origin === "diagnose" &&
      !group.identity.endsWith(`:${qualifier}`)
    ) {
      return false;
    }
  }
  // One rules result holds many rules and none states a resource; the rule's
  // name is the handle that tells them apart.
  if (observation.data.type === "alerts" && subject.name !== undefined)
    return observation.data.rule.name === subject.name;
  const identities = observationIdentities(observation);
  // An observation that states no resource of its own (an agent-run query
  // whose selectors do not name the target exactly, a topology summary) can
  // only be named by its evidence kind; the ref and the uniqueness rule do
  // the rest.
  if (identities.length === 0) return subject.observation !== undefined;
  return identities.some((identity) =>
    identityMatchesSubject(identity, observation, subject, targetPods),
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

// Pod names are DNS subdomains (letters, digits, hyphens, dots), so a name
// is a whole word between characters that cannot be part of one; "api"
// inside "api-other" names another Pod.
function namesPod(text: string, name: string): boolean {
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(^|[^A-Za-z0-9.-])${escaped}(?![A-Za-z0-9.-])`).test(text);
}

function identityMatchesSubject(
  identity: ObservationSubjectIdentity,
  observation: InvestigationEvidenceObservation,
  subject: DiagnosisEvidenceSubject,
  targetPods: ReadonlySet<string>,
): boolean {
  // "The events of namespace X" names a namespace-scoped read whose
  // observation inherits the resource its call was asked about; the agent
  // naming the namespace instead is not wrong, so a stated evidence kind plus
  // a matching namespace is enough for that shape.
  if (
    subject.observation !== undefined &&
    sameKind(subject.kind, "Namespace") &&
    identity.namespace !== undefined &&
    identity.namespace === subject.name
  )
    return true;
  // A workload read collects its pods' events, so "the events of Pod X" names
  // the events observation a diagnose bundle or workload call inherited from
  // the workload; the ref already fixes the call, uniqueness fixes the card.
  if (
    subject.observation !== undefined &&
    observation.data.type === "events" &&
    sameKind(subject.kind, "Pod") &&
    !sameKind(identity.kind, "Pod") &&
    (identity.namespace === undefined ||
      subject.namespace === undefined ||
      identity.namespace === subject.namespace) &&
    // The Pod has to be the target's own on events the target's read produced,
    // or named whole by an event: a Pod nothing here establishes is not this card.
    (subject.name === undefined ||
      (targetPods.has(subject.name) && observation.relevance !== "broader") ||
      observation.data.events.some((event) =>
        namesPod(event.message, subject.name!),
      ))
  )
    return true;
  if (!sameKind(identity.kind, subject.kind)) return false;
  // A subject with no name (a listing cited for what it does not contain) is
  // a wildcard that uniqueness still gates. A listing has no name of its own; the agent naming the entry it means
  // ("ConfigMap kube-root-ca.crt" in the ConfigMaps of a namespace) still
  // points at that listing, whether or not the listing holds the entry: an
  // absent entry is often the point, and the card says which it is.
  if (
    !identity.listing &&
    subject.name !== undefined &&
    identity.name !== subject.name
  )
    return false;
  // A built-in group on a core kind ("apps" on a Pod) is the agent
  // misremembering the API, not naming a different object: no CRD lives in a
  // built-in group, so nothing else could be meant. A vendor group on a core
  // kind (a Knative Service) still names a different object.
  if (
    subject.group !== undefined &&
    identity.group !== undefined &&
    subject.group.toLowerCase() !== identity.group.toLowerCase() &&
    !(identity.group === "" && isBuiltInGroup(subject.group.toLowerCase()))
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
