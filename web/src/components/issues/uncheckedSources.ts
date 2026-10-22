import { englishPlural } from "@skyhook-io/k8s-ui/utils/pluralize";
import type { IssuesResponse, UncheckedSource } from "../../api/client";

// The line each issue source gets wherever Radar's issues would otherwise read
// as complete, when nothing more specific can be said about the gap.
const SOURCE_LINES: Record<string, string> = {
  helm: "Helm releases couldn't be checked, so failed or stuck releases may be missing.",
  resources: "Some resources couldn't be checked, so their issues may be missing.",
  custom_resources: "Custom resources couldn't be checked, so failing ones may be missing.",
  karpenter: "Karpenter resources couldn't be checked, so NodePool and NodeClaim issues may be missing.",
};

// For a source this version doesn't know, sent by a newer Radar.
export const UNKNOWN_SOURCE_LINE = "Some issue checks couldn't run, so issues may be missing.";

const NAMED_LIMIT = 3;

/** "A", "A and B", "A, B and C", "A, B and 4 more". */
function nameList(names: string[]): string {
  if (names.length <= NAMED_LIMIT) {
    return names.length === 1 ? names[0] : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
  }
  return `${names.slice(0, NAMED_LIMIT - 1).join(", ")} and ${names.length - (NAMED_LIMIT - 1)} more`;
}

function helmLine({ reason, namespaces }: UncheckedSource): string {
  const where = namespaces?.length
    ? namespaces.length === 1
      ? `in ${namespaces[0]} `
      : `in ${namespaces.length} namespaces (${nameList(namespaces)}) `
    : "";
  const missing = where ? "failed or stuck releases there may be missing" : "failed or stuck releases may be missing";
  if (reason === "collector_access_denied") {
    return `Helm releases ${where}couldn't be checked because Radar's service account isn't allowed to impersonate users, so ${missing}.`;
  }
  return `Helm releases ${where}couldn't be checked, so ${missing}.`;
}

function kindsLine(source: UncheckedSource): string | undefined {
  const kinds = [...new Set((source.kinds ?? []).map((k) => englishPlural(k.kind)))];
  if (kinds.length === 0) {
    if (source.source === "custom_resources" && source.reason === "kind_sync_pending") {
      return "Custom resources are still loading, so failing ones may be missing.";
    }
    return undefined;
  }
  const names = nameList(kinds);
  switch (source.reason) {
    case "kind_sync_pending":
      return `Still loading ${names}, so their issues may be missing.`;
    case "kind_sync_failed":
      return `${names} couldn't be loaded, so their issues may be missing.`;
    default:
      return `${names} couldn't be checked, so their issues may be missing.`;
  }
}

function lineFor(source: UncheckedSource): string | undefined {
  if (!Object.hasOwn(SOURCE_LINES, source.source)) return undefined;
  if (source.source === "helm") return helmLine(source);
  return kindsLine(source) ?? SOURCE_LINES[source.source];
}

/** One line per unchecked source, known sources first, without repeats. */
export function uncheckedSourceLines(sources: IssuesResponse["unchecked_sources"]): string[] {
  const known = new Set<string>();
  let unknown = false;
  for (const source of sources ?? []) {
    const line = lineFor(source);
    if (line) known.add(line);
    else unknown = true;
  }
  return unknown ? [...known, UNKNOWN_SOURCE_LINE] : [...known];
}
