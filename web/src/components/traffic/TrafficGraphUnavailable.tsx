import type { ReactNode } from 'react'
import { RotateCw } from 'lucide-react'
import { AlertBanner } from '@skyhook-io/k8s-ui'
import type { AggregatedFlow } from '../../types'
import { TrafficConnectionsTable } from './TrafficConnectionsTable'
import type { TrafficGraphSelection } from './TrafficGraph'
import type { TrafficFocus } from './trafficFilters'

interface TrafficGraphUnavailableProps {
  /** The view's connections as they are, before addons are grouped for drawing. */
  flows: AggregatedFlow[]
  isRateBased: boolean
  selection: TrafficGraphSelection | null
  onSelect: (selection: TrafficGraphSelection | null) => void
  onFocus: (focus: TrafficFocus) => void
  focusedId?: string
  /** Shown above the explanation, in the layout rather than over it. */
  notice?: ReactNode
}

/**
 * Stands in for the map when its layout engine could not be fetched, which is
 * what a tab opened before an upgrade gets: says so, offers the reload that
 * fixes it, and lists the connections so the view still answers something.
 */
export function TrafficGraphUnavailable({ flows, isRateBased, selection, onSelect, onFocus, focusedId, notice }: TrafficGraphUnavailableProps) {
  return (
    <div className="absolute inset-x-0 bottom-0 top-12 flex flex-col px-3 pb-3 gap-2">
      {notice}
      <AlertBanner
        variant="warning"
        className="mb-0"
        title="The traffic map could not load its layout engine"
        message="Radar may have been updated since this page was opened. Reload to draw the map; the connections are listed below."
        action={
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="btn-brand-muted inline-flex items-center gap-1 text-xs px-2.5 py-1 rounded-md"
          >
            <RotateCw className="h-3.5 w-3.5" /> Reload
          </button>
        }
      />
      <div className="min-h-0 flex-1 overflow-hidden rounded-lg border border-theme-border bg-theme-surface">
        <TrafficConnectionsTable
          flows={flows}
          isRateBased={isRateBased}
          selection={selection}
          onSelect={onSelect}
          onFocus={onFocus}
          focusedId={focusedId}
        />
      </div>
    </div>
  )
}
