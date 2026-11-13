import type { ReactNode } from 'react'
import { AlertBanner, Badge, RadarUpgradeNote, countLabel } from '@skyhook-io/k8s-ui'
import type { LogInstance, LogPipelineProvider, LogPodCount, LogWorkload } from '@skyhook-io/k8s-ui/types/core'
import { useLoggingPipeline } from '../../api/loggingPipeline'
import { RadarFeatureUnsupportedError } from '../../api/radarFeatures'
import type { WorkspaceScreenProps } from '../../integrations/workspaceRoute'

const KIND_LABELS: Record<string, string> = {
  loggings: 'Loggings', fluentbitagents: 'FluentbitAgents', clusterflows: 'ClusterFlows', flows: 'Flows',
  clusteroutputs: 'ClusterOutputs', outputs: 'Outputs', daemonsets: 'DaemonSets', statefulsets: 'StatefulSets',
}
const PLURALS: Record<string, string> = { ClusterFlow: 'clusterflows', Flow: 'flows', ClusterOutput: 'clusteroutputs', Output: 'outputs' }

function Count({ value, what }: { value: LogPodCount; what: string }) {
  const { text, title } = countLabel(value, what)
  return <span title={title} className="tabular-nums">{text}</span>
}

function Step({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="min-w-0 flex-1 space-y-1.5">
      <div className="text-xs font-medium uppercase tracking-wide text-theme-text-tertiary">{title}</div>
      {children}
    </div>
  )
}

const Arrow = () => <div className="self-center text-theme-text-tertiary" aria-hidden>→</div>

function Workloads({ items, empty, open }: { items: LogWorkload[]; empty: string; open: WorkspaceScreenProps['onOpenResource'] }) {
  if (items.length === 0) return <div className="text-sm text-theme-text-tertiary">{empty}</div>
  return (
    <>
      {items.map((w) => (
        <div key={`${w.kind}/${w.name}`} className="flex items-center gap-1.5 text-sm">
          <button className="truncate text-left text-accent-text hover:underline" onClick={() => open({ kind: w.kind === 'DaemonSet' ? 'daemonsets' : 'statefulsets', namespace: w.namespace, name: w.name, group: 'apps' })}>{w.name}</button>
          <Badge severity={w.ready >= w.desired && w.desired > 0 ? 'success' : 'warning'} size="sm">{w.ready}/{w.desired}</Badge>
        </div>
      ))}
    </>
  )
}

function Instance({ provider, instance, open }: { provider: LogPipelineProvider; instance: LogInstance; open: WorkspaceScreenProps['onOpenResource'] }) {
  const routes = provider.routes.filter((r) => r.instance === instance.name)
  const named = new Set(routes.flatMap((r) => r.destinations.map((d) => `${d.kind}/${d.namespace ?? ''}/${d.name}`)))
  const destinations = provider.destinations.filter((d) => named.has(`${d.kind}/${d.namespace}/${d.name}`))
  return (
    <section className="card-inner space-y-3">
      <div className="text-sm text-theme-text-secondary">
        Logging <span className="font-medium text-theme-text-primary">{instance.name}</span> · control namespace {instance.controlNamespace}
      </div>
      <div className="flex flex-col gap-3 md:flex-row">
        <Step title="Collectors"><Workloads items={instance.collectors} empty="None found" open={open} /></Step>
        <Arrow />
        <Step title="Aggregator"><Workloads items={instance.aggregators} empty="None found" open={open} /></Step>
        <Arrow />
        <Step title="Routes · running pods">
          {routes.length === 0 && <div className="text-sm text-theme-text-tertiary">No routes</div>}
          {routes.map((r) => (
            <div key={`${r.kind}/${r.namespace}/${r.name}`} className="flex flex-wrap items-center gap-1.5 text-sm">
              <button className="text-left text-accent-text hover:underline" onClick={() => open({ kind: PLURALS[r.kind] ?? 'flows', namespace: r.namespace, name: r.name, group: r.group })}>{`${r.kind} ${r.name}`}</button>
              {r.ignored ? <Badge severity="neutral" size="sm">ignored by the operator</Badge> : <Count value={r.selectedPods} what="running pods selected" />}
              {r.active === false && <Badge severity="error" size="sm">inactive</Badge>}
            </div>
          ))}
        </Step>
        <Arrow />
        <Step title="Destinations">
          {destinations.length === 0 && <div className="text-sm text-theme-text-tertiary">None found</div>}
          {destinations.map((d) => (
            <div key={`${d.kind}/${d.namespace}/${d.name}`} className="space-y-0.5 text-sm">
              <div className="flex flex-wrap items-center gap-1.5">
                <button className="text-left text-accent-text hover:underline" onClick={() => open({ kind: PLURALS[d.kind] ?? 'outputs', namespace: d.namespace, name: d.name, group: provider.routes[0]?.group })}>{d.name}</button>
                {d.type && <span className="text-theme-text-tertiary">{d.type}</span>}
                {d.active === false && <Badge severity="error" size="sm">inactive</Badge>}
                {d.active === undefined && <Badge severity="neutral" size="sm">state not reported</Badge>}
              </div>
              {d.address && <div className="truncate text-xs text-theme-text-tertiary">{d.address}</div>}
              {d.problems.map((p) => <div key={p} className="text-xs text-theme-text-secondary">{p}</div>)}
            </div>
          ))}
        </Step>
      </div>
    </section>
  )
}

/** The /logging workspace: how logs flow through each installed shipper, and which running pods nothing routes. */
export function LoggingView({ namespaces, onOpenResource }: WorkspaceScreenProps) {
  const { data, error, isLoading } = useLoggingPipeline(namespaces)
  let body: ReactNode
  if (error instanceof RadarFeatureUnsupportedError) {
    body = <RadarUpgradeNote requirement={error.radarUpgrade} />
  } else if (error) {
    body = <AlertBanner variant="warning" title="Could not read the logging pipeline" message={(error as Error).message} />
  } else if (!data) {
    body = <div className="text-sm text-theme-text-tertiary">{isLoading ? 'Reading the logging pipeline…' : 'No answer yet.'}</div>
  } else if (data.providers.length === 0) {
    body = <div className="text-sm text-theme-text-tertiary">No log shipper Radar understands is installed. Radar reads the Logging operator (logging.banzaicloud.io).</div>
  } else {
    const unread = data.coverage.gaps.map((g) => KIND_LABELS[g.resource] ?? g.resource)
    body = (
      <div className="space-y-4">
        {unread.length > 0 && (
          <AlertBanner variant="info" title={`Radar could not read ${unread.join(', ')}`} message="What is missing here is left out of the diagram, and pod counts are lower bounds." className="mb-0" />
        )}
        {data.providers.map((p) => p.instances.map((i) => <Instance key={`${p.provider}/${i.name}`} provider={p} instance={i} open={onOpenResource} />))}
        {data.providers.flatMap((p) => p.routes.filter((r) => !r.instance)).length > 0 && (
          <div className="text-sm text-theme-text-secondary">
            Not processed (no Logging has their loggingRef):{' '}
            {data.providers.flatMap((p) => p.routes.filter((r) => !r.instance)).map((r) => `${r.kind} ${r.namespace}/${r.name}`).join(', ')}
          </div>
        )}
        <section className="card-inner space-y-2">
          <div className="text-sm font-medium text-theme-text-primary">
            Running pods nothing routes: <Count value={data.unrouted} what="running pods whose logs no route selects" />
          </div>
          <div className="text-xs text-theme-text-tertiary">Their logs are lost with the pod.{data.unrouted.truncated && ` Showing the first ${data.unrouted.pods.length}.`}</div>
          {data.unrouted.pods.length > 0 && (
            <table className="w-full text-sm">
              <thead><tr className="text-left text-xs text-theme-text-tertiary"><th className="py-1 font-normal">Namespace</th><th className="py-1 font-normal">Pod</th></tr></thead>
              <tbody>
                {data.unrouted.pods.map((p) => (
                  <tr key={`${p.namespace}/${p.name}`} className="border-t border-theme-border">
                    <td className="py-1 text-theme-text-secondary">{p.namespace}</td>
                    <td className="py-1"><button className="text-left text-accent-text hover:underline" onClick={() => onOpenResource({ kind: 'pods', namespace: p.namespace, name: p.name })}>{p.name}</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      </div>
    )
  }
  return (
    <div className="flex-1 space-y-4 overflow-auto p-4">
      <h1 className="text-lg font-semibold text-theme-text-primary">Logging pipeline</h1>
      {namespaces.length > 0 && <div className="text-xs text-theme-text-tertiary">Pod counts for namespace {namespaces.join(', ')}</div>}
      {body}
    </div>
  )
}
