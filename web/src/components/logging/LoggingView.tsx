import type { ReactNode } from 'react'
import { AlertBanner, Badge, RadarUpgradeNote, Tooltip, countLabel } from '@skyhook-io/k8s-ui'
import type { LogDestination, LogInstance, LogPipelineProvider, LogPipelineRoute, LogPodCount, LogWorkload } from '@skyhook-io/k8s-ui/types/core'
import { useLoggingPipeline } from '../../api/loggingPipeline'
import { RadarFeatureUnsupportedError } from '../../api/radarFeatures'
import type { WorkspaceScreenProps } from '../../integrations/workspaceRoute'

const KIND_LABELS: Record<string, string> = {
  pods: 'Pods', loggings: 'Loggings', fluentbitagents: 'FluentbitAgents', clusterflows: 'ClusterFlows', flows: 'Flows',
  clusteroutputs: 'ClusterOutputs', outputs: 'Outputs', daemonsets: 'DaemonSets', statefulsets: 'StatefulSets',
}
const PLURALS: Record<string, string> = { ClusterFlow: 'clusterflows', Flow: 'flows', ClusterOutput: 'clusteroutputs', Output: 'outputs' }

function Count({ value, what }: { value: LogPodCount; what: string }) {
  const { text, title } = countLabel(value, what)
  return <Tooltip content={title}><span className="tabular-nums">{text}</span></Tooltip>
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

type Open = WorkspaceScreenProps['onOpenResource']

function RouteRows({ routes, open }: { routes: LogPipelineRoute[]; open: Open }) {
  if (routes.length === 0) return <div className="text-sm text-theme-text-tertiary">No routes</div>
  return (
    <>
      {routes.map((r) => (
        <div key={`${r.kind}/${r.namespace}/${r.name}`} className="flex flex-wrap items-center gap-1.5 text-sm">
          <button className="text-left text-accent-text hover:underline" onClick={() => open({ kind: PLURALS[r.kind] ?? 'flows', namespace: r.namespace, name: r.name, group: r.group })}>{`${r.kind} ${r.name}`}</button>
          {r.ignored ? <Badge severity="neutral" size="sm">ignored by the operator</Badge> : <Count value={r.selectedPods} what="running pods selected" />}
          {r.active === false && <Badge severity="error" size="sm">inactive</Badge>}
        </div>
      ))}
    </>
  )
}

// Every destination the routes name, with its details when Radar read it, so a
// route sending to a missing (or unread) output shows that rather than nothing.
function DestinationRows({ provider, routes, gaps, open }: { provider: LogPipelineProvider; routes: LogPipelineRoute[]; gaps: string[]; open: Open }) {
  const refs = new Map<string, LogDestination>()
  for (const d of routes.flatMap((r) => r.destinations)) refs.set(`${d.kind}/${d.namespace ?? ''}/${d.name}`, d)
  if (refs.size === 0) return <div className="text-sm text-theme-text-tertiary">None named</div>
  return (
    <>
      {[...refs.values()].map((ref) => {
        const d = provider.destinations.find((x) => x.kind === ref.kind && x.namespace === (ref.namespace ?? '') && x.name === ref.name)
        const key = `${ref.kind}/${ref.namespace ?? ''}/${ref.name}`
        if (!d) {
          const unread = gaps.includes(PLURALS[ref.kind] ?? '')
          return (
            <div key={key} className="flex flex-wrap items-center gap-1.5 text-sm">
              <span className="text-theme-text-primary">{ref.name}</span>
              <Badge severity={unread ? 'neutral' : 'error'} size="sm">{unread ? 'not read' : 'not found'}</Badge>
            </div>
          )
        }
        return (
          <div key={key} className="space-y-0.5 text-sm">
            <div className="flex flex-wrap items-center gap-1.5">
              <button className="text-left text-accent-text hover:underline" onClick={() => open({ kind: PLURALS[d.kind] ?? 'outputs', namespace: d.namespace, name: d.name, group: routes[0]?.group })}>{d.name}</button>
              {d.type && <span className="text-theme-text-tertiary">{d.type}</span>}
              {d.active === false && <Badge severity="error" size="sm">inactive</Badge>}
              {d.active === undefined && <Badge severity="neutral" size="sm">state not reported</Badge>}
            </div>
            {d.address && <div className="truncate text-xs text-theme-text-tertiary">{d.address}</div>}
            {d.problems.map((p) => <div key={p} className="text-xs text-theme-text-secondary">{p}</div>)}
          </div>
        )
      })}
    </>
  )
}

function Instance({ provider, instance, gaps, open }: { provider: LogPipelineProvider; instance: LogInstance; gaps: string[]; open: Open }) {
  const routes = provider.routes.filter((r) => r.instance === instance.name)
  return (
    <section className="card-inner space-y-3">
      <div className="text-sm text-theme-text-secondary">
        Logging <span className="font-medium text-theme-text-primary">{instance.name}</span> · control namespace {instance.controlNamespace}
      </div>
      <div className="flex flex-col gap-3 md:flex-row">
        <Step title="Collectors"><Workloads items={instance.collectors} empty={instance.collectorsUnread ? 'Not read' : 'None found'} open={open} /></Step>
        <Arrow />
        <Step title="Aggregator"><Workloads items={instance.aggregators} empty={instance.aggregatorsUnread ? 'Not read' : 'None found'} open={open} /></Step>
        <Arrow />
        <Step title="Routes · running pods"><RouteRows routes={routes} open={open} /></Step>
        <Arrow />
        <Step title="Destinations"><DestinationRows provider={provider} routes={routes} gaps={gaps} open={open} /></Step>
      </div>
    </section>
  )
}

// Routes no Logging Radar read carries: unprocessed when the Loggings were
// read, unplaceable when they weren't.
function UnplacedRoutes({ provider, gaps, open }: { provider: LogPipelineProvider; gaps: string[]; open: Open }) {
  const routes = provider.routes.filter((r) => !r.instance)
  if (routes.length === 0) return null
  return (
    <section className="card-inner space-y-2">
      <div className="text-sm text-theme-text-secondary">
        {gaps.includes('loggings')
          ? 'Loggings could not be read, so these routes can\'t be placed in a pipeline. Their pods count only as may be selected.'
          : 'Not processed: no Logging has their loggingRef.'}
      </div>
      <RouteRows routes={routes} open={open} />
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
    const gaps = data.coverage.gaps.map((g) => g.resource)
    const unread = gaps.map((g) => KIND_LABELS[g] ?? g)
    body = (
      <div className="space-y-4">
        {unread.length > 0 && (
          <AlertBanner variant="info" title={`Radar could not read ${unread.join(', ')}`} message="What is missing here is left out of the diagram, and pod counts are lower bounds." className="mb-0" />
        )}
        {data.providers.map((p) => (
          <div key={p.provider} className="space-y-4">
            {p.instances.map((i) => <Instance key={i.name} provider={p} instance={i} gaps={gaps} open={onOpenResource} />)}
            <UnplacedRoutes provider={p} gaps={gaps} open={onOpenResource} />
          </div>
        ))}
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
