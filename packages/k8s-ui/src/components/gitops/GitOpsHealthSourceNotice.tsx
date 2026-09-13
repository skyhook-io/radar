import { Info } from 'lucide-react'
import type { ReactNode } from 'react'
import type { GitOpsInsightSummary } from '../../types'

// One quiet line above the Issues band for the two cases where per-resource
// health on this page is not the controller's own verdict:
//
//  - Argo CD keeps per-resource health outside the Application object (its
//    3.0+ default). Radar then reads the live resources itself, and every
//    problem below is Radar's finding — the "Radar" markers say so per row;
//    this line says so once.
//  - The Application deploys to another cluster. Radar can't read its
//    resources from here at all, so nothing is derived. A standalone Radar
//    may add a pointer to Radar Cloud (the host passes it; embedded hosts
//    pass nothing).
//
// Copy stays in plain words; the field names live behind the docs link.

export const APP_TREE_HEALTH_NOTICE =
  "Argo CD isn't sharing per-resource health with Radar, so the problems listed here are what Radar found on its own."
export const REMOTE_DESTINATION_NOTICE =
  "This application deploys to another cluster, so Radar can't inspect its resources from here."

export type HealthSourceNoticeSummary = Pick<GitOpsInsightSummary, 'tool' | 'health' | 'resourceHealthMode' | 'remoteDestination'>

// The appTree notice explains problems Radar found on its own; on an app
// Argo calls Healthy nothing is derived (the host doesn't run the overlay
// there), so the line would explain nothing and is left out.
export function healthSourceNoticeKind(summary: HealthSourceNoticeSummary | undefined): 'remote' | 'appTree' | null {
  if (!summary || summary.tool !== 'argocd') return null
  if (summary.remoteDestination) return 'remote'
  if (summary.resourceHealthMode === 'appTree' && summary.health !== 'Healthy') return 'appTree'
  return null
}

export function GitOpsHealthSourceNotice({
  summary,
  docsUrl,
  remoteDestinationHint,
}: {
  summary: HealthSourceNoticeSummary | undefined
  // Where "Why?" goes for the appTree case. Omitted → no link.
  docsUrl?: string
  // Rendered after the remote-destination sentence. The host decides
  // whether there is anything to say (standalone Radar points at Radar
  // Cloud; an embedded host passes nothing).
  remoteDestinationHint?: ReactNode
}) {
  const kind = healthSourceNoticeKind(summary)
  if (!kind) return null
  return (
    <div className="flex shrink-0 items-start gap-2 border-b border-theme-border bg-theme-base px-4 py-2 text-xs text-theme-text-secondary" role="note">
      <Info className="mt-px h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
      <span className="min-w-0">
        {kind === 'remote' ? (
          <>
            {REMOTE_DESTINATION_NOTICE}
            {remoteDestinationHint ? <> {remoteDestinationHint}</> : null}
          </>
        ) : (
          <>
            {APP_TREE_HEALTH_NOTICE}
            {docsUrl && (
              <>
                {' '}
                <a href={docsUrl} target="_blank" rel="noreferrer" className="underline decoration-theme-border underline-offset-2 hover:text-theme-text-primary">
                  Why?
                </a>
              </>
            )}
          </>
        )}
      </span>
    </div>
  )
}
