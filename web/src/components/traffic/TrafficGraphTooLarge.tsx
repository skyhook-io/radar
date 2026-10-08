import { useEffect, useMemo, useRef, useState } from 'react'
import { ChevronDown, Network } from 'lucide-react'
import { pluralNoun } from '@skyhook-io/k8s-ui'
import { SEVERITY_TEXT } from '@skyhook-io/k8s-ui/utils/badge-colors'
import type { AggregatedFlow } from '../../types'
import { LargeClusterNamespacePicker } from '../shared/LargeClusterNamespacePicker'
import { TrafficConnectionsTable } from './TrafficConnectionsTable'
import { TrafficFocusSearch } from './TrafficFocusSearch'
import type { TrafficGraphSelection } from './TrafficGraph'
import type { EndpointSummary, NamespaceSummary, TrafficFocus } from './trafficFilters'

const count = (n: number, noun: string) => `${n.toLocaleString()} ${pluralNoun(n, noun)}`

interface TrafficGraphTooLargeProps {
  graph: { nodes: number; edges: number }
  /** The focused endpoint's name, when the view is a neighborhood. */
  focusName?: string
  onDrawAnyway?: () => void
  /** Null when the namespace can't be changed from here (a single namespace
   *  in view, or a deployment scoped to one). */
  namespaces: NamespaceSummary[] | null
  onPickNamespace: (namespace: string) => void
  endpoints: EndpointSummary[]
  onFocus: (focus: TrafficFocus) => void
  isRateBased: boolean
  overlayContainer?: HTMLElement | null
  flows: AggregatedFlow[]
  selection: TrafficGraphSelection | null
  onSelect: (selection: TrafficGraphSelection | null) => void
  focusedId?: string
}

/**
 * Stands in for the map when it is too large to lay out: says why, offers the
 * ways to narrow it, and lists the connections so the view still answers
 * something.
 */
export function TrafficGraphTooLarge(props: TrafficGraphTooLargeProps) {
  const { graph, focusName, onDrawAnyway, namespaces, onPickNamespace, endpoints, onFocus, isRateBased, overlayContainer } = props
  const [pickerOpen, setPickerOpen] = useState(false)
  const pickerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!pickerOpen) return
    const handler = (e: MouseEvent) => {
      if (pickerRef.current && !pickerRef.current.contains(e.target as Node)) setPickerOpen(false)
    }
    document.addEventListener('mousedown', handler, true)
    return () => document.removeEventListener('mousedown', handler, true)
  }, [pickerOpen])

  const statsByName = useMemo(() => new Map((namespaces ?? []).map(ns => [ns.name, ns])), [namespaces])

  return (
    <div className="absolute inset-x-0 bottom-0 top-12 flex flex-col px-3 pb-3 gap-2">
      <div className="rounded-lg border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
        <div className="flex items-start gap-3">
          <Network className="mt-0.5 h-5 w-5 shrink-0 text-theme-text-tertiary" />
          <div className="min-w-0 flex-1">
            <div className="text-sm font-medium text-theme-text-primary">
              {focusName ? `${focusName} has too many connections to draw` : 'Too much traffic to draw at once'}
            </div>
            <p className="mt-0.5 text-xs text-theme-text-secondary">
              {count(graph.nodes, 'endpoint')} and {count(graph.edges, 'connection')} — more than the map can lay out without freezing this tab.
              {' '}{focusName
                ? 'Its connections are listed below; focus on one of them, or narrow with the filters on the left.'
                : 'They are listed below, problems first. Narrow to a namespace or focus on a workload to see a map.'}
            </p>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              {namespaces && namespaces.length > 0 && (
                <div className="relative" ref={pickerRef}>
                  <button
                    type="button"
                    onClick={() => setPickerOpen(o => !o)}
                    aria-expanded={pickerOpen}
                    className="btn-brand-muted inline-flex items-center gap-1 text-xs px-2.5 py-1 rounded-md"
                  >
                    Narrow to a namespace <ChevronDown className="h-3.5 w-3.5" />
                  </button>
                  {pickerOpen && (
                    <div className="absolute left-0 top-full z-50 mt-1 w-80 rounded-lg border border-theme-border bg-theme-surface p-2 shadow-theme-lg">
                      <LargeClusterNamespacePicker
                        namespaces={namespaces}
                        ranked
                        onSelect={ns => {
                          setPickerOpen(false)
                          onPickNamespace(ns)
                        }}
                        renderMeta={name => {
                          const ns = statsByName.get(name)
                          if (!ns) return null
                          return (
                            <span className="flex shrink-0 items-center gap-1.5 text-[10px] tabular-nums text-theme-text-tertiary">
                              {ns.drops + ns.errors > 0 && <span className={SEVERITY_TEXT.error}>{(ns.drops + ns.errors).toLocaleString()} failing</span>}
                              <span>{count(ns.endpoints, 'endpoint')}</span>
                            </span>
                          )
                        }}
                      />
                      <p className="mt-1 px-1 text-[10px] text-theme-text-tertiary">
                        Sets the namespace for all of Radar. Traffic to and from other namespaces stays in view.
                      </p>
                    </div>
                  )}
                </div>
              )}
              <TrafficFocusSearch
                endpoints={endpoints}
                onFocus={onFocus}
                isRateBased={isRateBased}
                overlayContainer={overlayContainer}
                variant="panel"
              />
              {onDrawAnyway && (
                <button
                  type="button"
                  onClick={onDrawAnyway}
                  className="text-xs px-2.5 py-1 rounded-md border border-theme-border text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover"
                >
                  Draw anyway
                </button>
              )}
            </div>
          </div>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-hidden rounded-lg border border-theme-border bg-theme-surface">
        <TrafficConnectionsTable
          flows={props.flows}
          isRateBased={isRateBased}
          selection={props.selection}
          onSelect={props.onSelect}
          onFocus={onFocus}
          focusedId={props.focusedId}
        />
      </div>
    </div>
  )
}
