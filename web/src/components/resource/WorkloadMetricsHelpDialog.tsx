import { useLayoutEffect, useId, useRef } from 'react'
import { createPortal } from 'react-dom'
import { ExternalLink, X } from 'lucide-react'
import { Disclosure } from '@skyhook-io/k8s-ui/components/ui/Disclosure'
import type { WorkloadMetrics } from '../../api/workloadMetrics'
import type { PrometheusTimeRange } from '../../api/client'

interface Props {
  kind: string
  namespace: string
  name: string
  range: PrometheusTimeRange
  data?: WorkloadMetrics
  pending: boolean
  error?: Error | null
  onClose: () => void
}

export function WorkloadMetricsHelpDialog({ onClose, ...props }: Props) {
  const dialog = useRef<HTMLDialogElement>(null)
  const backdropPointerDown = useRef(false)
  const titleId = useId()
  useLayoutEffect(() => {
    const element = dialog.current!
    element.showModal()
    return () => element.close()
  }, [])

  return createPortal(
    <dialog
      ref={dialog}
      aria-labelledby={titleId}
      className="dialog m-auto w-[800px] max-w-[calc(100vw-2rem)] max-h-[85dvh] p-0 text-theme-text-primary open:flex open:flex-col backdrop:bg-black/60 backdrop:backdrop-blur-sm"
      onCancel={(event) => { event.preventDefault(); onClose() }}
      onKeyDown={(event) => event.stopPropagation()}
      onPointerDown={(event) => {
        const bounds = event.currentTarget.getBoundingClientRect()
        backdropPointerDown.current = event.target === event.currentTarget && (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom)
      }}
      onClick={(event) => {
        if (!backdropPointerDown.current || event.target !== event.currentTarget) return
        backdropPointerDown.current = false
        const bounds = event.currentTarget.getBoundingClientRect()
        if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) onClose()
      }}
    >
      <header className="flex shrink-0 items-start justify-between gap-4 border-b border-theme-border px-6 py-4">
        <div className="min-w-0">
          <h2 id={titleId} className="text-lg font-semibold">Metrics sources &amp; coverage</h2>
          <p className="mt-1 break-words text-sm text-theme-text-tertiary">{props.kind} · {props.namespace}/{props.name} · {props.range}</p>
        </div>
        <button type="button" autoFocus onClick={onClose} aria-label="Close metrics help" className="rounded-md p-1.5 text-theme-text-secondary hover:bg-theme-hover focus-visible:outline-accent">
          <X aria-hidden="true" className="h-5 w-5" />
        </button>
      </header>
      <div className="min-h-0 overflow-y-auto px-6 py-5">
        <WorkloadMetricsHelpContent {...props} />
      </div>
      <footer className="shrink-0 border-t border-theme-border px-6 py-3">
        <a className="inline-flex items-center gap-1.5 text-sm text-accent-text hover:underline" href="https://github.com/skyhook-io/radar/blob/main/docs/workload-metrics.md#what-each-chart-needs" target="_blank" rel="noopener noreferrer">
          What each chart needs <ExternalLink aria-hidden="true" className="h-3.5 w-3.5" />
        </a>
      </footer>
    </dialog>, document.body,
  )
}

const evidenceLabels: Record<string, string> = {
  cpu: 'CPU', memory: 'Memory', throttling: 'Throttling', beyla: 'Beyla · HTTP requests', istio: 'Istio · HTTP requests',
}

export function WorkloadMetricsHelpContent({ data, pending, error }: Pick<Props, 'data' | 'pending' | 'error'>) {
  const evidence = Object.entries(data?.attribution ?? {}).filter(([key]) => key !== 'scope' && key !== 'history')
  const resourceRows = new Map<string, string[]>()
  for (const [key, reason] of evidence) {
    if (!['cpu', 'memory', 'throttling'].includes(key)) continue
    const labels = resourceRows.get(reason) ?? []
    labels.push(evidenceLabels[key])
    resourceRows.set(reason, labels)
  }
  const rows = [
    ...Array.from(resourceRows, ([reason, labels]) => ({ label: labels.join(' / '), reason })),
    ...evidence.filter(([key]) => !['cpu', 'memory', 'throttling'].includes(key)).map(([key, reason]) => ({ label: evidenceLabels[key] ?? key, reason })),
  ]
  const comparisonNotices = Array.from(new Set(Object.values(data?.comparison ?? {})
    .filter((panel) => panel.state === 'detecting' || panel.state === 'error')
    .map((panel) => panel.reason || (panel.state === 'detecting' ? 'Matching current-Pod metrics…' : 'Current-Pod comparison could not be loaded.'))))
  const emptyReason = data && ['detecting', 'error', 'unavailable'].includes(data.state) ? data.reason : undefined
  return <div className="space-y-6 text-sm leading-relaxed text-theme-text-secondary">
    <section>
      <h3 className="mb-1 font-semibold text-theme-text-primary">Source attribution details</h3>
      <p className="text-theme-text-tertiary">Sources are checked independently. Identity matching does not mean every Pod has samples throughout the time window.</p>
      {pending && <p role="status" className="mt-3">Checking the selected metrics…{data && ' Evidence below is from the previous response.'}</p>}
      {error && <p role="status" className="mt-3 text-warning-text">Matching evidence could not be {data ? 'refreshed' : 'loaded'}: {error.message}{data && ' Evidence below is from the previous response.'}</p>}
      {rows.length > 0 ? <table className="mt-3 w-full table-fixed text-left">
        <thead className="text-xs text-theme-text-tertiary"><tr>
          <th scope="col" className="w-1/3 border-b border-theme-border pb-2 pr-4 font-medium">Charts / source</th>
          <th scope="col" className="border-b border-theme-border pb-2 font-medium">Current-Pod matching</th>
        </tr></thead>
        <tbody>{rows.map(({ label, reason }) => <tr key={label} className="border-b border-theme-border-subtle">
          <th scope="row" className="py-3 pr-4 align-top font-medium text-theme-text-primary">{label}</th>
          <td className="break-words py-3 align-top">{reason}</td>
        </tr>)}</tbody>
      </table> : !pending && !error && !data?.attribution?.scope && comparisonNotices.length === 0 && <p className="mt-3">{emptyReason || 'No current-Pod matching evidence is available yet.'}</p>}
      {comparisonNotices.length > 0 && <div className="mt-3">
        <h4 className="font-medium text-theme-text-primary">Current-Pod comparison</h4>
        {comparisonNotices.map((reason) => <p role="status" key={reason}>{reason}</p>)}
      </div>}
      {data?.attribution?.scope && <div className="mt-3 border-l-2 border-theme-border pl-3">
        <h4 className="font-medium text-theme-text-primary">Operator-asserted scope</h4>
        <p>{data.attribution.scope}</p>
      </div>}
      {data?.attribution?.history && <div className="mt-3 border-l-2 border-theme-border pl-3">
        <h4 className="font-medium text-theme-text-primary">Historical ownership</h4>
        <p>{data.attribution.history}</p>
        <p className="mt-1 text-theme-text-tertiary">Each chart’s history label reports whether that chart can use retained ownership.</p>
      </div>}
    </section>
    <section className="border-t border-theme-border pt-5">
      <h3 className="mb-3 font-semibold text-theme-text-primary">Reading the charts</h3>
      <dl className="grid grid-cols-1 gap-x-6 gap-y-4 min-[600px]:grid-cols-2">
        <div><dt className="font-medium text-theme-text-primary">Workload history vs current Pods</dt><dd className="mt-1">History follows retained ownership at each timestamp, including previous replicas. “Current Pods only” excludes previous replicas. Recreation under the same workload name is included in history.</dd></div>
        <div><dt className="font-medium text-theme-text-primary">Missing is not zero</dt><dd className="mt-1">Unmatched Pods are excluded. Missing ownership or samples produces gaps. An idle workload and an uninstrumented one can both have no HTTP observations.</dd></div>
        <div><dt className="font-medium text-theme-text-primary">HTTP observations</dt><dd className="mt-1">Ports and processes are combined, including health checks and admin traffic. HTTP 5xx is not a gRPC error rate and excludes failures without an HTTP response. Latency is measured at the selected observer, not end to end.</dd></div>
        <div><dt className="font-medium text-theme-text-primary">Resource usage</dt><dd className="mt-1">CPU and memory include reporting containers and sidecars. Their historical charts show the workload total and maximum Pod; current-Pod charts show individual Pods. Throttling measures CFS periods, not CPU time lost.</dd></div>
      </dl>
    </section>
    <Disclosure className="border-t border-theme-border pt-4" summaryClassName="font-medium text-theme-text-primary" chevronClassName="h-4 w-4" summary="Troubleshooting identity matching">
      <div className="mt-3 space-y-3">
        <p>Check the affected chart’s warning first. Scope overrides address cluster identity; they do not add missing metrics, instrumentation or ownership history.</p>
        <p>If automatic matching cannot establish identity, an operator can explicitly assert the backend’s scope:</p>
        <dl className="space-y-3">
          <div><dt className="font-medium">Backend dedicated to this cluster</dt><dd><code className="break-all font-mono text-xs">--prometheus-single-cluster</code><p className="mt-1">Helm: <code className="break-all font-mono text-xs">traffic.prometheusSingleCluster: true</code></p></dd></div>
          <div><dt className="font-medium">Shared backend with a cluster label</dt><dd><code className="break-all font-mono text-xs">--prometheus-cluster-label cluster=your-cluster</code><p className="mt-1">Helm: set the backend’s actual label and value in <code className="break-all font-mono text-xs">traffic.prometheusClusterLabels</code>.</p></dd></div>
        </dl>
        <p>Leave both unset for automatic matching. An override replaces automatic matching for workload requests, resources, history and Pod comparison only; it does not scope rightsizing or other metrics features.</p>
        <h4 className="font-medium text-theme-text-primary">Operator configuration</h4>
        <p><strong className="font-medium">Standalone CLI:</strong> supply the flag when starting Radar. Overrides are not saved; after changing the cluster, backend or credentials, verify the new scope before restarting with a fresh override.</p>
        <p><strong className="font-medium">In-cluster OSS or Radar Cloud:</strong> ask the installation’s operator to update its Helm values or GitOps configuration. These are shared backend settings, not viewer preferences.</p>
        <p><strong className="font-medium">Desktop:</strong> automatic matching works without flags. Desktop does not expose these overrides yet; if one is required, use the standalone CLI with a verified scope.</p>
        <p><strong className="font-medium">Multiple Beyla observation jobs:</strong> workload charts currently honor <code className="font-mono text-xs">--beyla-job-selector</code> only alongside a verified scope override. Use one matcher, such as <code className="font-mono text-xs">{'\'job="primary-beyla"\''}</code>; Helm uses <code className="font-mono text-xs">traffic.beylaJobSelector</code>. A custom job name alone normally needs no override.</p>
      </div>
    </Disclosure>
  </div>
}
