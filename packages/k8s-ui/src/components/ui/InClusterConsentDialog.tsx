import { useState } from 'react'
import { ConfirmDialog } from './ConfirmDialog'
import { rememberInClusterConsent } from '../../utils/inClusterConsent'

/** One request the in-cluster job will actually send. */
export interface ConsentRequest {
  /** Route label, for the operator to recognise which path this is. */
  route: string
  /** Concrete request, e.g. "GET https://checkout.example.com/healthz". */
  request: string
}

interface InClusterConsentDialogProps {
  open: boolean
  cluster?: string
  namespace: string
  /** The requests this run will send, one per declared route. */
  requests?: ConsentRequest[]
  /** Declared paths the run covers but for which no concrete request could be
   *  derived. They still count toward the scope the operator is agreeing to. */
  untestedCount?: number
  /** Server-side ceiling on probe Pods per call. */
  maxProbes?: number
  onClose: () => void
  onConfirm: () => void
}

/** Rows are capped so the dialog stays a confirmation, not a report. */
const MAX_LISTED_REQUESTS = 6

/**
 * The fact block the operator confirms against. It is the last screen before
 * real pod-to-pod traffic, so it must name the requests being sent - it used to
 * show only where the Job would land, and then only a single "GET /" that was
 * wrong in the DEFAULT case: with no path override the server keeps each
 * route's OWN declared path, so "/" is least accurate exactly when it is shown.
 */
export function inClusterConsentDetails(o: {
  cluster?: string
  namespace: string
  requests?: ConsentRequest[]
  untestedCount?: number
  /** Server-side ceiling on probe Pods per call. Routes past it do not run, and
   *  since ordering is deterministic they are not reached by re-running either -
   *  so the screen must say so rather than imply full coverage. */
  maxProbes?: number
}): string {
  const reqs = o.requests ?? []
  const untested = o.untestedCount ?? 0
  const total = reqs.length + untested
  const cap = o.maxProbes ?? 0
  const willRun = cap > 0 ? Math.min(reqs.length, cap) : reqs.length
  const lines = [cluster(o.cluster), `Namespace: ${o.namespace}`]
  if (total > 0) {
    lines.push(`Declared:  ${total} path${total === 1 ? '' : 's'} on this resource`)
    // "0 requests, one Pod each" is nonsense, and it describes a run that would
    // create nothing at all - so say that instead of counting to zero.
    lines.push(
      willRun === 0
        ? 'Will run:  nothing — no request could be derived from these paths'
        : `Will run:  ${willRun} request${willRun === 1 ? '' : 's'}, one Pod each`,
    )
  }
  for (const r of reqs.slice(0, MAX_LISTED_REQUESTS)) {
    lines.push(`  → ${r.request}`)
  }
  const hidden = reqs.length - Math.min(reqs.length, MAX_LISTED_REQUESTS)
  if (hidden > 0) lines.push(`  → …and ${hidden} more`)
  // Named, never silently dropped: they are part of the scope being agreed to
  // even though no concrete request could be derived for them.
  if (untested > 0) {
    lines.push(`  → ${untested} path${untested === 1 ? '' : 's'} with no derivable request`)
  }
  const beyond = reqs.length - willRun
  if (beyond > 0) {
    lines.push(`  → ${beyond} beyond the ${cap}-Pod limit — not tested, and re-running starts from the same first ${cap}`)
  }
  return lines.filter(Boolean).join('\n')
}

function cluster(name?: string): string {
  return name ? `Cluster:   ${name}` : ''
}

// Confirms the mutating in-cluster reachability test before it spawns a Job/pod,
// naming the cluster it lands in. Permission is enforced upstream (the button only
// renders when the capability SSAR allows), so this is a safety confirm, not authz.
export function InClusterConsentDialog({ open, cluster, namespace, requests, untestedCount, maxProbes, onClose, onConfirm }: InClusterConsentDialogProps) {
  const [dontAskAgain, setDontAskAgain] = useState(false)

  function handleClose() {
    onClose()
    setDontAskAgain(false)
  }

  function handleConfirm() {
    if (dontAskAgain && cluster) rememberInClusterConsent(cluster)
    setDontAskAgain(false)
    onConfirm()
  }

  return (
    <ConfirmDialog
      open={open}
      onClose={handleClose}
      onConfirm={handleConfirm}
      // Wide enough for a real cluster identity: a GKE context name runs past
      // 50 characters and wrapped mid-name at the default width.
      className="w-full max-w-xl"
      title="Run in-cluster reachability test?"
      message="This creates one short-lived, self-deleting Job per request, sending real pod-to-pod traffic from inside the cluster. Each Pod runs under the target namespace’s default ServiceAccount with no token mounted — not as you — and your cluster may inject sidecars into it."
      details={inClusterConsentDetails({ cluster, namespace, requests, untestedCount, maxProbes })}
      // Nothing to send means nothing to confirm. The button was live for a run
      // that would create no Pods and change no evidence.
      confirmDisabled={(requests?.length ?? 0) === 0}
      confirmLabel="Run test"
      cancelLabel="Cancel"
      variant="warning"
    >
      {/* "Don't ask again" is stored per cluster. Without a cluster identity
          there is nothing safe to key it on (one origin can front many
          clusters), so consent is never remembered - hide the checkbox rather
          than promise a persistence that doesn't stick. */}
      {cluster ? (
        <label className="flex items-center gap-2 text-sm text-theme-text-secondary">
          <input
            type="checkbox"
            checked={dontAskAgain}
            onChange={(e) => setDontAskAgain(e.target.checked)}
            className="w-4 h-4 rounded border-theme-border bg-theme-base text-amber-600 focus:ring-amber-500 focus:ring-offset-0"
          />
          <span>Don&apos;t ask again for this cluster</span>
        </label>
      ) : (
        <p className="text-xs text-theme-text-tertiary">
          Cluster identity unavailable - this confirmation can&apos;t be saved, so it will be asked on every run.
        </p>
      )}
    </ConfirmDialog>
  )
}
