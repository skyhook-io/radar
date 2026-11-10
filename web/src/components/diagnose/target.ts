import { resourceKey } from "@skyhook-io/k8s-ui";
import { groupQualifiesLaneId } from "@skyhook-io/k8s-ui/utils/navigation";

import {
  kindToPluralWithGroup,
  knownKindForPluralWithGroup,
  pluralToKind,
} from "../../utils/navigation";

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

// A custom resource is shown as Kind.api.group, the Kubernetes-qualified
// identity users can copy, because its Kind can collide with another
// group's. A built-in group never collides, so its Kind stands alone.
export function formatInvestigationTarget(
  target: InvestigationTargetIdentity,
): string {
  const kind = formatInvestigationKind(target.kind, target.group);
  return `${kind} ${target.namespace ? `${target.namespace}/` : ""}${target.name}`;
}

export function formatInvestigationKind(kind: string, group: string): string {
  const canonicalKind = knownKindForPluralWithGroup(kind, group) ?? kind;
  return groupQualifiesLaneId(group)
    ? `${canonicalKind}.${group}`
    : canonicalKind;
}

export interface InvestigationOpening extends InvestigationTargetIdentity {
  question?: string;
  namespaces?: string[];
  lookBack?: string;
  askedAt?: string;
}

// When a run's question was first asked: its own start, unless it re-ran or
// narrowed an earlier question. A run built from it keeps that time, so its
// look-back still covers the incident the engineer described.
// Radar accepts an asking time from the last 30 days; an older question is
// asked again from now rather than refused.
const ASKED_AT_LIMIT_MS = 29 * 24 * 60 * 60 * 1000;

export function questionAskedAt(
  run: InvestigationOpening & { createdAt: string },
  now = Date.now(),
): string | undefined {
  if (!run.question) return undefined;
  const askedAt = run.askedAt ?? run.createdAt;
  const age = now - Date.parse(askedAt);
  return age >= 0 && age < ASKED_AT_LIMIT_MS ? askedAt : undefined;
}

// A run opened by a question alone has no resource: no Apply, no
// per-resource "running" badge, and its question is its title.
export function isQuestionOnly(run: InvestigationOpening): boolean {
  return !run.name && !!run.question;
}

// The question's own words name a question-only run; a run about a resource
// is named by the resource even when it started with a question, because the
// resource is what a reader scanning the history recognises.
export function formatInvestigationTitle(run: InvestigationOpening): string {
  return isQuestionOnly(run)
    ? (run.question ?? "").replace(/\s+/g, " ").trim()
    : formatInvestigationTarget(run);
}

// Where and since when a question said to start, in the words the composer's
// chips use: "shop, payments · last 1h".
// A run carrying an earlier asking time looks back from then, not from now,
// so it says when: "shop · 1h before Oct 27, 3:04 PM".
export function formatQuestionScope(
  run: InvestigationOpening,
  now = new Date(),
): string {
  const where = run.namespaces?.length
    ? run.namespaces.join(", ")
    : "All namespaces";
  if (!run.lookBack) return where;
  const asked = run.askedAt ? new Date(run.askedAt) : null;
  if (!asked || Number.isNaN(asked.getTime())) {
    return `${where} · last ${run.lookBack}`;
  }
  const sameDay = asked.toDateString() === now.toDateString();
  const when = asked.toLocaleString(undefined, {
    ...(sameDay ? {} : { month: "short", day: "numeric" }),
    hour: "numeric",
    minute: "2-digit",
  });
  return `${where} · ${run.lookBack} before ${when}`;
}

// Investigate again: a new session about the same thing, started the same
// way. It carries the issue and the question forward and nothing the earlier
// run found — being handed the previous answer is the one thing someone
// clicking it doesn't want.
export function rerunOpening(
  run: InvestigationOpening & { issueId?: string; createdAt: string },
  now = Date.now(),
): InvestigationOpening & { issueId?: string; fresh: true } {
  return {
    kind: run.kind,
    group: run.group,
    namespace: run.namespace,
    name: run.name,
    issueId: run.issueId,
    fresh: true,
    question: run.question,
    namespaces: run.namespaces,
    lookBack: run.lookBack,
    askedAt: questionAskedAt(run, now),
  };
}

// Callers name kinds every way Kubernetes accepts: a route's plural
// (deployments), an agent's lowercase singular (deployment), or the Kind.
// A resource someone will read about is named by its Kind.
export function canonicalKind(kind: string, group: string): string {
  // Resolve within the group first: metrics.k8s.io's "pods" are PodMetrics,
  // not Pods.
  const plural = kindToPluralWithGroup(kind, group);
  return knownKindForPluralWithGroup(plural, group) ?? pluralToKind(plural);
}
