import type { RunSummary } from "../../api/diagnose";
import { isQuestionOnly, runTargetKey } from "./target";

// Past this age a verdict describes the cluster as it was, not as it is, so the
// resource stops pointing at it. History still lists it.
export const LAST_INVESTIGATION_MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;

export interface LastInvestigation {
  id: string;
  preview: string;
  assessedAt: string;
}

// The newest assessment of each resource on the cluster in use. Only runs the
// server dated with assessedAt qualify: their preview is the assessment
// Findings shows, not a later answer. Stale runs count, since switching
// clusters marks every run stale.
export function lastInvestigations(
  runs: readonly RunSummary[],
  context: string | undefined,
  now: number,
): Map<string, LastInvestigation> {
  const out = new Map<string, LastInvestigation>();
  if (!context) return out;
  for (const r of runs) {
    if (
      isQuestionOnly(r) ||
      r.status === "running" ||
      r.status === "stopping" ||
      r.context !== context ||
      !r.assessedAt ||
      !r.preview
    )
      continue;
    const assessed = Date.parse(r.assessedAt);
    if (!(now - assessed <= LAST_INVESTIGATION_MAX_AGE_MS)) continue;
    const key = runTargetKey(r.kind, r.namespace, r.name, r.group);
    const prev = out.get(key);
    if (prev && Date.parse(prev.assessedAt) >= assessed) continue;
    out.set(key, {
      id: r.id,
      preview: r.preview,
      assessedAt: r.assessedAt,
    });
  }
  return out;
}

// Whether the object on screen is the one the assessment describes. Without a
// UID the creation time is the only tell: an object created after the
// assessment was recreated since. Unknown creation time can't rule it out.
export function sameObject(
  last: LastInvestigation,
  objectCreatedAt: string | undefined,
): boolean {
  if (!objectCreatedAt) return true;
  return !(Date.parse(objectCreatedAt) > Date.parse(last.assessedAt));
}
