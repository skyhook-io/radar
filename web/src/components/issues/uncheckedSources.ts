import type { IssuesResponse } from "../../api/client";

// The issue sources /api/issues can list in unchecked_sources, and the line
// each one gets wherever Radar's issues would otherwise read as complete.
const SOURCE_LINES: Record<string, string> = {
  helm: "Helm releases couldn't be checked, so failed or stuck releases may be missing.",
};

// For a source this version doesn't know, sent by a newer Radar.
export const UNKNOWN_SOURCE_LINE = "Some issue checks couldn't run, so issues may be missing.";

/** One line per unchecked source, known sources first, without repeats. */
export function uncheckedSourceLines(sources: IssuesResponse["unchecked_sources"]): string[] {
  const known = new Set<string>();
  let unknown = false;
  for (const { source } of sources ?? []) {
    if (Object.hasOwn(SOURCE_LINES, source)) known.add(SOURCE_LINES[source]);
    else unknown = true;
  }
  return unknown ? [...known, UNKNOWN_SOURCE_LINE] : [...known];
}
