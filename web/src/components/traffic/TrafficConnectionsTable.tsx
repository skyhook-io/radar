import { useMemo } from 'react'
import { Virtuoso } from 'react-virtuoso'
import { clsx } from 'clsx'
import { ArrowRight, Crosshair, Minus } from 'lucide-react'
import type { AggregatedFlow } from '../../types'
import { SEVERITY_TEXT } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { Tooltip } from '../ui/Tooltip'
import type { TrafficGraphSelection } from './TrafficGraph'
import {
  connectionRows, displayVolume, flowDrops, flowErrors, formatRate, graphEndpointId, hasStatusData, isExternalKind,
  type TrafficFocus,
} from './trafficFilters'

interface TrafficConnectionsTableProps {
  flows: AggregatedFlow[]
  isRateBased: boolean
  selection: TrafficGraphSelection | null
  onSelect: (selection: TrafficGraphSelection | null) => void
  onFocus: (focus: TrafficFocus) => void
  /** The focused endpoint, whose name is not offered as a focus again. */
  focusedId?: string
}

const GRID = 'grid grid-cols-[minmax(0,1fr)_1rem_minmax(0,1fr)_6.5rem_5rem_4.5rem_4.5rem] items-center gap-x-3'
// Without HTTP status anywhere in view (plain TCP) a 5xx column would read as
// "no errors" rather than "not measured".
const GRID_NO_STATUS = 'grid grid-cols-[minmax(0,1fr)_1rem_minmax(0,1fr)_6.5rem_5rem_4.5rem] items-center gap-x-3'

function rowSelection(flow: AggregatedFlow): TrafficGraphSelection {
  return {
    type: 'edge',
    sourceId: graphEndpointId(flow.source),
    destId: graphEndpointId(flow.destination),
    port: flow.port,
    directionUnknown: !!flow.directionUnknown,
  }
}

function sameEdge(a: TrafficGraphSelection, b: TrafficGraphSelection | null): boolean {
  return !!b && b.type === 'edge' && a.sourceId === b.sourceId && a.destId === b.destId &&
    a.port === b.port && !!a.directionUnknown === !!b.directionUnknown
}

/**
 * The view's connections as rows, for when there are too many to draw. Where
 * the problems are comes first. Selecting a row loads its records into the
 * flow list, as selecting an edge does; a name narrows the view to it.
 */
export function TrafficConnectionsTable({ flows, isRateBased, selection, onSelect, onFocus, focusedId }: TrafficConnectionsTableProps) {
  const rows = useMemo(() => connectionRows(flows), [flows])
  const showStatus = useMemo(() => flows.some(hasStatusData), [flows])
  const grid = showStatus ? GRID : GRID_NO_STATUS

  const endpoint = (e: AggregatedFlow['source']) => {
    const id = graphEndpointId(e)
    const label = (
      <span className="min-w-0">
        <span className="block truncate text-theme-text-primary">{e.name}</span>
        {e.namespace && <span className="block truncate text-[10px] text-theme-text-tertiary">{e.namespace}</span>}
      </span>
    )
    if (id === focusedId || e.kind === 'Internet') return <span className="flex min-w-0 items-center">{label}</span>
    return (
      <Tooltip content={`Focus on ${e.name}`} wrapperClassName="min-w-0">
        <button
          type="button"
          onClick={ev => {
            ev.stopPropagation()
            onFocus({ namespace: e.namespace || undefined, name: e.name })
          }}
          className="group flex min-w-0 max-w-full items-center gap-1 text-left hover:underline"
        >
          {label}
          <Crosshair className="h-3 w-3 shrink-0 opacity-0 group-hover:opacity-70" />
        </button>
      </Tooltip>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className={clsx(grid, 'px-3 py-1.5 text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary border-b border-theme-border')}>
        <span>Source</span>
        <span />
        <span>Destination</span>
        <span>Port</span>
        <span className="text-right">{isRateBased ? 'Req/s' : 'Conns'}</span>
        {showStatus && <span className="text-right">5xx</span>}
        <span className="text-right">Dropped</span>
      </div>
      <Virtuoso
        className="flex-1"
        data={rows}
        computeItemKey={(_, flow) => `${graphEndpointId(flow.source)}->${graphEndpointId(flow.destination)}:${flow.port}${flow.directionUnknown ? ':u' : ''}`}
        itemContent={(_, flow) => {
          const sel = rowSelection(flow)
          const selected = sameEdge(sel, selection)
          const errors = flowErrors(flow)
          const drops = flowDrops(flow)
          return (
            <div
              role="row"
              aria-selected={selected}
              onClick={() => onSelect(selected ? null : sel)}
              className={clsx(
                grid,
                'cursor-pointer px-3 py-1.5 text-xs border-b border-theme-border/50 transition-colors',
                selected ? 'bg-theme-elevated' : 'hover:bg-theme-hover',
              )}
            >
              {endpoint(flow.source)}
              {flow.directionUnknown
                ? <Tooltip content="Direction unknown"><Minus className="h-3 w-3 text-theme-text-tertiary" /></Tooltip>
                : <ArrowRight className="h-3 w-3 text-theme-text-tertiary" />}
              {endpoint(flow.destination)}
              <span className="truncate text-theme-text-secondary tabular-nums">
                {flow.port > 0 ? flow.port : '—'}
                {(flow.l7Protocol || flow.protocol) && (
                  <span className="ml-1 text-theme-text-tertiary">{flow.l7Protocol || flow.protocol.toUpperCase()}</span>
                )}
                {isExternalKind(flow.destination.kind) && <span className="ml-1 text-theme-text-tertiary">ext</span>}
              </span>
              <span className="text-right tabular-nums text-theme-text-secondary">
                {isRateBased ? formatRate(displayVolume(flow, true)) : flow.connections.toLocaleString()}
              </span>
              {showStatus && (
                <span className={clsx('text-right tabular-nums', errors > 0 ? clsx(SEVERITY_TEXT.error, 'font-medium') : 'text-theme-text-tertiary')}>
                  {errors > 0 ? (isRateBased ? formatRate(errors) : errors.toLocaleString()) : hasStatusData(flow) ? '0' : '—'}
                </span>
              )}
              <span className={clsx('text-right tabular-nums', drops > 0 ? clsx(SEVERITY_TEXT.error, 'font-medium') : 'text-theme-text-tertiary')}>
                {drops > 0 ? drops.toLocaleString() : '—'}
              </span>
            </div>
          )
        }}
      />
    </div>
  )
}
