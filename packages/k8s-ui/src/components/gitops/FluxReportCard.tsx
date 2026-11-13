import { useState } from 'react'
import type { FluxReportSummary } from '../../utils/flux-report'
import { formatRelativeAgeTime } from '../../utils/format'
import { Collapse, CollapseChevron, useDisclosure } from '../ui/Collapse'
import { StatusDot } from '../ui/status-tone'
import { AlertBanner } from '../ui/drawer-components'

const OPEN_KEY = 'radar.gitops.fluxCard.open'

function readOpen(): boolean {
  if (typeof window === 'undefined') return false
  try {
    return window.localStorage.getItem(OPEN_KEY) === 'true'
  } catch {
    return false
  }
}

// FluxReportCard sums up Flux Operator's FluxReport above the GitOps table:
// one line folded, versions, controllers and what Flux reconciles open.
export function FluxReportCard({
  summary,
  instance,
  onOpenResource,
  defaultOpen,
}: {
  summary: FluxReportSummary
  instance?: { namespace: string; name: string; version?: string } | null
  onOpenResource?: (ref: { kind: string; namespace: string; name: string; group: string }) => void
  defaultOpen?: boolean
}) {
  const [open, setOpen] = useState(() => defaultOpen ?? readOpen())
  const toggle = () => {
    setOpen((was) => {
      try {
        window.localStorage.setItem(OPEN_KEY, String(!was))
      } catch {
        // storage unavailable: the card still toggles for this view
      }
      return !was
    })
  }
  const disclosure = useDisclosure(open)
  const version = summary.version ?? 'unknown version'
  return (
    <div className="mx-4 mt-3 rounded-md border border-theme-border bg-theme-surface text-sm">
      <button
        {...disclosure.buttonProps}
        type="button"
        onClick={toggle}
        className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-left hover:bg-theme-hover"
      >
        <CollapseChevron open={open} className="h-3.5 w-3.5" />
        <span className="font-medium text-theme-text-primary">
          Flux {version} ·{' '}
          {summary.controllersReady === undefined ? 'controllers unknown' : `${summary.controllersReady} of ${summary.controllers.length} controllers ready`}
          {summary.failing !== undefined && ` · ${summary.failing} failing`}
        </span>
        {summary.reportedAt && <span className="text-xs text-theme-text-tertiary">reported {formatRelativeAgeTime(summary.reportedAt)}</span>}
        {summary.total > 1 && <span className="text-xs text-theme-text-tertiary">1 of {summary.total}</span>}
      </button>
      <Collapse open={open} id={disclosure.panelId}>
        <div className="space-y-3 border-t border-theme-border px-3 py-3">
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-theme-text-secondary">
            {summary.managedBy ? (
              <span>installed by {summary.managedBy} {summary.operatorVersion ?? ''}</span>
            ) : (
              summary.operatorVersion && <span>operator {summary.operatorVersion}</span>
            )}
            {instance && (
              <span>
                FluxInstance{' '}
                {onOpenResource ? (
                  <button type="button" className="text-accent hover:underline" onClick={() => onOpenResource({ kind: 'fluxinstances', namespace: instance.namespace, name: instance.name, group: 'fluxcd.controlplane.io' })}>
                    {instance.namespace}/{instance.name}
                  </button>
                ) : (
                  `${instance.namespace}/${instance.name}`
                )}
                {instance.version && ` tracks ${instance.version}`}
              </span>
            )}
          </div>
          {summary.needsInventoryNote && (
            <AlertBanner
              variant="info"
              title="Inventory needs Flux 2.8"
              className="mb-0"
              message={`HelmRelease inventory needs Flux 2.8. This cluster runs ${version}, so Radar attributes HelmRelease resources by Flux labels and shows "Tracked by".`}
            />
          )}
          <div className="grid gap-4 md:grid-cols-2">
            <div>
              <div className="mb-1 text-xs font-medium text-theme-text-tertiary">Controllers</div>
              <div className="space-y-0.5">
                {summary.controllers.map((c) => (
                  <div key={c.name} className="flex items-center gap-2">
                    <StatusDot tone={c.ready ? 'healthy' : 'unhealthy'} />
                    <span className="text-theme-text-primary">{c.name}</span>
                    {c.tag && <span className="font-mono text-xs text-theme-text-tertiary">{c.tag}</span>}
                  </div>
                ))}
              </div>
            </div>
            <div>
              <div className="mb-1 text-xs font-medium text-theme-text-tertiary">What Flux reconciles</div>
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-left text-theme-text-tertiary">
                    <th className="py-0.5 pr-2 font-medium">Kind</th>
                    <th className="py-0.5 pr-2 font-medium">Running</th>
                    <th className="py-0.5 pr-2 font-medium">Failing</th>
                    <th className="py-0.5 font-medium">Suspended</th>
                  </tr>
                </thead>
                <tbody>
                  {summary.rows.map((r) => (
                    <tr key={r.kind} className="border-t border-theme-border/60">
                      <td className="py-0.5 pr-2 text-theme-text-primary">{r.kind}</td>
                      <td className="py-0.5 pr-2 text-theme-text-secondary">{r.running}</td>
                      <td className="py-0.5 pr-2 text-theme-text-secondary">{r.failing}</td>
                      <td className="py-0.5 text-theme-text-secondary">{r.suspended}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {summary.also.length > 0 && (
                <div className="mt-1 text-xs text-theme-text-tertiary">
                  Also: {summary.also.map((a) => `${a.kind} ${a.running}`).join(', ')}
                </div>
              )}
            </div>
          </div>
        </div>
      </Collapse>
    </div>
  )
}
