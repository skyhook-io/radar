// summarizeInClusterTests reduces the per-route in-cluster outcome rows to what
// the banner needs. The server (internal/reachability/incluster.go) returns one
// row per route with three distinct shapes that must NOT be conflated:
//
//   FOLDED      results && !status  - the probe ran clean and was folded into the
//                                     merged trace (the default branch). The route
//                                     outcome actually moved.
//   EVIDENCE    results && status   - the probe ran and produced results but was
//                                     NOT folded: a throwaway-pod NetworkPolicy/mTLS
//                                     denial, an override-mismatch (custom path), or
//                                     a guessed wildcard path. Route outcome UNCHANGED.
//   COULDN'T-RUN !results && (status||fallback) - the Job couldn't start / timed out /
//                                     RBAC / pod-cap / budget. Nothing was tested.
//
// From these, the banner has exactly three honest states:
//   couldn't-run   - a COULDN'T-RUN row exists and nothing folded
//   partial        - a COULDN'T-RUN row exists but other routes DID fold
//   evidence-only  - rows carried results but NONE folded (route outcomes unchanged)
//   (clean full run - everything folded - shows no banner)

export interface InClusterTestRow {
  status?: string
  fallbackCommand?: string
  results?: unknown[]
}

export interface InClusterSummary {
  /** A route that genuinely could NOT be tested in-cluster (Job failed / capped /
   *  budget). Paired with `fallback`. Absent when every route at least ran. */
  error?: string
  fallback?: string
  /** ≥1 route folded live results into the merged trace AND ≥1 route could not run.
   *  The banner reads "partially completed" instead of the false "couldn't run". */
  partial: boolean
  /** Rows carried results but NONE folded (throwaway-denied / override-mismatch /
   *  guessed path) - the run changed no route outcome. Surfaced as an informational
   *  note so an all-evidence run isn't a silent no-op. */
  evidenceOnly: boolean
  /** The evidence row's status message (the honest per-shape explanation). */
  evidence?: string
}

const isFolded = (t: InClusterTestRow): boolean => !!t.results?.length && !t.status
const isEvidence = (t: InClusterTestRow): boolean => !!t.results?.length && !!t.status
const isCouldntRun = (t: InClusterTestRow): boolean => !t.results?.length && (!!t.status || !!t.fallbackCommand)

export function summarizeInClusterTests(tests: InClusterTestRow[] | undefined): InClusterSummary {
  const entries = tests ?? []
  const anyFolded = entries.some(isFolded)
  const failed = entries.find(isCouldntRun)
  if (failed) {
    return {
      error: failed.status || 'the in-cluster test could not run',
      fallback: failed.fallbackCommand,
      partial: anyFolded,
      evidenceOnly: false,
    }
  }
  // No hard failure. Any evidence row means at least one route's outcome did
  // NOT move - surface its honest per-shape explanation. This deliberately
  // includes the mixed case (some routes folded, some kept informational):
  // suppressing the note there presented a partially-inconclusive run as a
  // clean one, with the kept-informational route's status silently vanished.
  const evidence = entries.find(isEvidence)
  if (evidence) {
    // partial: some routes DID fold - the banner must not say "unchanged".
    return { partial: anyFolded, evidenceOnly: true, evidence: evidence.status, fallback: evidence.fallbackCommand }
  }
  // Clean full run - everything folded; the merged trace already reflects the
  // outcomes, so there is nothing left to explain.
  return { partial: false, evidenceOnly: false }
}

/** The evidence banner's title. Kept beside the reducer so the words cannot
 *  drift from the state: "unchanged" was still shown for mixed runs where some
 *  routes HAD updated. */
export function evidenceBannerTitle(s: InClusterSummary): string {
  return s.partial
    ? 'In-cluster test: some routes updated — others kept informational'
    : 'In-cluster test ran as evidence only - route outcomes unchanged'
}
