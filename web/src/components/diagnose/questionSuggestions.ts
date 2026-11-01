import {
  compareIssues,
  issueTitle,
  type Issue,
  type IssueSeverity,
} from "@skyhook-io/k8s-ui";

import { formatInvestigationTarget } from "./target";

export interface QuestionPin {
  kind: string;
  group: string;
  namespace: string;
  name: string;
}

export interface QuestionSuggestion {
  key: string;
  /** The problem as the Issues page names it, so the row is recognizable. */
  title: string;
  /** Where it is: one resource, or how many resources in which namespace. */
  where: string;
  severity: IssueSeverity;
  /** What goes in the box. A pinned resource is named by its chip instead. */
  question: string;
  namespaces: string[];
  /** Set when the suggestion is about one resource, so the run can start
   *  there instead of searching for it. */
  pin?: QuestionPin;
}

// Questions an engineer would plausibly ask about what Radar sees right now,
// most severe first: one per problem per namespace. A problem on one
// resource becomes a question about that resource; the same problem on
// several becomes a question about the namespace. A resource with two issues
// of the same name is still one resource. A cluster with no issues
// gets no suggestions — a stock prompt would only be noise.
export function suggestionsFromIssues(
  issues: readonly Issue[],
  max = 3,
): QuestionSuggestion[] {
  const groups = new Map<string, Issue[]>();
  for (const issue of [...issues].sort(compareIssues)) {
    const key = `${issueTitle(issue)}\u0000${issue.namespace ?? ""}`;
    const members = groups.get(key);
    if (members) members.push(issue);
    else groups.set(key, [issue]);
  }
  // One kind of problem per suggestion first, so three namespaces with the
  // same symptom don't crowd out a different one; then fill by severity.
  const seen = new Set<string>();
  const firstOfKind: [string, Issue[]][] = [];
  const rest: [string, Issue[]][] = [];
  for (const entry of groups) {
    const category = entry[1][0].category;
    if (seen.has(category)) rest.push(entry);
    else {
      seen.add(category);
      firstOfKind.push(entry);
    }
  }
  const suggestions: QuestionSuggestion[] = [];
  for (const [key, members] of [...firstOfKind, ...rest]) {
    if (suggestions.length >= max) break;
    const first = members[0];
    const title = issueTitle(first);
    const namespaces = first.namespace ? [first.namespace] : [];
    const resources = new Set(
      members.map((m) => `${m.kind}\u0000${m.group ?? ""}\u0000${m.name}`),
    ).size;
    if (resources === 1) {
      const pin = {
        kind: first.kind,
        group: first.group ?? "",
        namespace: first.namespace ?? "",
        name: first.name,
      };
      suggestions.push({
        key,
        title,
        where: formatInvestigationTarget(pin),
        severity: severityOf(first),
        question: `What's causing “${title}”?`,
        namespaces,
        pin,
      });
      continue;
    }
    const scope = first.namespace
      ? `in ${first.namespace}`
      : "across the cluster";
    suggestions.push({
      key,
      title,
      where: first.namespace
        ? `${resources} resources · ${first.namespace}`
        : `${resources} resources across the cluster`,
      severity: severityOf(first),
      question: `What's causing “${title}” on ${resources} resources ${scope}?`,
      namespaces,
    });
  }
  return suggestions;
}

// Questions about a pinned resource, from the issues Radar reports for it
// (its own, and ones on related objects that name it). The resource stays
// pinned; the question names the problem, and the object it is on when that
// is not the pinned resource.
export function suggestionsForPin(
  issues: readonly Issue[],
  pin: QuestionPin,
  max = 3,
): QuestionSuggestion[] {
  const pinned = formatInvestigationTarget(pin);
  const seen = new Set<string>();
  const suggestions: QuestionSuggestion[] = [];
  for (const issue of [...issues].sort(compareIssues)) {
    if (suggestions.length >= max) break;
    const title = issueTitle(issue);
    const on = formatInvestigationTarget({
      kind: issue.kind,
      group: issue.group ?? "",
      namespace: issue.namespace ?? "",
      name: issue.name,
    });
    const key = `${title}\u0000${on}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const question =
      on === pinned
        ? `What's causing “${title}”?`
        : `What's causing “${title}” on ${on}?`;
    suggestions.push({
      key,
      title,
      where: on,
      severity: severityOf(issue),
      question,
      namespaces: [],
      pin,
    });
  }
  return suggestions;
}

// The Issues page coerces an unknown severity to warning; so does this list.
function severityOf(issue: Issue): IssueSeverity {
  return issue.severity === "critical" ? "critical" : "warning";
}
