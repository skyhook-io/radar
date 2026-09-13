import { Info } from 'lucide-react'
import type { ReactNode } from 'react'
import type { GitOpsChange, GitOpsInsightSummary } from '../../types'

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
  'Argo CD 3 no longer records health for each resource on the application. Rows marked "Found by Radar" are problems Radar detected by looking at the resources itself.'
// Same situation, but Radar's own look found nothing to mark: the app is
// Degraded by Argo's reckoning and nothing here says which resource. The
// one thing the user can do today is name-checked, because the page has
// nothing else to offer.
export const APP_TREE_NO_FINDINGS_NOTICE =
  'Argo CD 3 no longer records health for each resource on the application, and Radar didn\'t find a problem on its own. To see Argo\'s per-resource health here, connect Radar to your Argo CD server in Settings, or set controller.resource.health.persist to "true" in argocd-cmd-params-cm.'
export const REMOTE_DESTINATION_NOTICE =
  "This application deploys to a different cluster, so its resources aren't visible from here."

export type HealthSourceNoticeSummary = Pick<GitOpsInsightSummary, 'tool' | 'health' | 'resourceHealthMode' | 'remoteDestination' | 'resourceHealthFromApi'>

// The appTree notice explains problems Radar found on its own; on an app
// Argo calls Healthy nothing is derived (the host doesn't run the overlay
// there), and when the host read Argo's verdicts from its API server there
// is nothing of Radar's to explain either.
export function healthSourceNoticeKind(summary: HealthSourceNoticeSummary | undefined): 'remote' | 'appTree' | null {
  if (!summary || summary.tool !== 'argocd') return null
  if (summary.remoteDestination) return 'remote'
  if (summary.resourceHealthMode === 'appTree' && !summary.resourceHealthFromApi && summary.health !== 'Healthy') return 'appTree'
  return null
}

// Reads the same rows the "Found by Radar" markers read, so the sentence
// and the markers can't disagree — an Issue can be suppressed (an operation
// failure already names the resource) while the row still carries Radar's
// health.
export function hasRadarFinding(changes: GitOpsChange[] | undefined): boolean {
  return (changes ?? []).some((c) => c.healthSource === 'radar' && (c.health === 'Degraded' || c.health === 'Missing'))
}

export function GitOpsHealthSourceNotice({
  summary,
  changes,
  docsUrl,
  remoteDestinationHint,
}: {
  summary: HealthSourceNoticeSummary | undefined
  // Decides between the two appTree sentences: with a Radar-sourced
  // problem row the notice explains the markers; without one it says so
  // and names the way out.
  changes?: GitOpsChange[]
  // Where "Why?" goes for the appTree case. Omitted → no link.
  docsUrl?: string
  // Rendered after the remote-destination sentence. The host decides
  // whether there is anything to say (standalone Radar points at Radar
  // Cloud; an embedded host passes nothing).
  remoteDestinationHint?: ReactNode
}) {
  const kind = healthSourceNoticeKind(summary)
  if (!kind) return null
  // With nothing marked, the sentence only earns its place on an app Argo
  // calls Degraded: that is the badge it explains. A Progressing or Missing
  // app with no Radar finding has nothing here to attribute.
  const radarFinding = hasRadarFinding(changes)
  if (kind === 'appTree' && !radarFinding && summary?.health !== 'Degraded') return null
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
            {radarFinding ? APP_TREE_HEALTH_NOTICE : APP_TREE_NO_FINDINGS_NOTICE}
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
