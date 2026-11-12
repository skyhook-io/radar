import { Boxes, History, Layers, Server, Workflow } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, ProblemAlerts, ResourceLink } from '../../ui/drawer-components'
import { GitOpsStatusBadge, ManagedResourcesList } from '../../gitops'
import { fluxConditionsToGitOpsStatus, parseFluxInventory, type FluxCondition } from '../../../types/gitops'
import { formatRelativeAgeTime } from '../../../utils/format'
import { imageTag } from '../../../utils/flux-report'
import { redactUrlCredentials } from '../resource-utils-flux-operator'

type NavigateRef = { kind: string; namespace: string; name: string; group?: string }
interface Props {
  data: any
  onNavigate?: (ref: NavigateRef) => void
}

function OperatorStatus({ data }: { data: any }) {
  const conditions = (data.status?.conditions || []) as FluxCondition[]
  const status = fluxConditionsToGitOpsStatus(conditions, data.metadata?.annotations?.['fluxcd.controlplane.io/reconcile'] === 'disabled', {
    generation: data.metadata?.generation,
    observedGeneration: data.status?.observedGeneration,
  })
  const problems: Array<{ color: 'red' | 'yellow'; message: string }> = []
  if (status.health === 'Degraded' && status.message) problems.push({ color: 'red', message: status.message })
  return (
    <>
      <ProblemAlerts problems={problems} />
      <Section title="Status">
        <GitOpsStatusBadge status={status} showHealth={false} />
      </Section>
    </>
  )
}

function Inventory({ entries }: { entries?: Array<{ id: string; v: string }> }) {
  const resources = parseFluxInventory(entries || [])
  if (resources.length === 0) return null
  return <ManagedResourcesList resources={resources} title={`Inventory (${resources.length} resources)`} />
}

const yesNo = (v: unknown) => (v === undefined ? undefined : v ? 'Yes' : 'No')

export function FluxInstanceRenderer({ data }: Props) {
  const spec = data.spec || {}
  const status = data.status || {}
  const components: any[] = status.components || []
  const history: any[] = status.history || []
  return (
    <>
      <OperatorStatus data={data} />
      <Section title="Distribution" icon={Layers}>
        <PropertyList>
          <Property label="Version" value={spec.distribution?.version} />
          <Property label="Registry" value={spec.distribution?.registry} />
          <Property label="Applied" value={status.lastAppliedRevision} />
          {status.lastAttemptedRevision && status.lastAttemptedRevision !== status.lastAppliedRevision && (
            <Property label="Attempted" value={status.lastAttemptedRevision} />
          )}
        </PropertyList>
      </Section>
      {components.length > 0 && (
        <Section title={`Components (${components.length})`} icon={Boxes}>
          <div className="space-y-1 text-sm">
            {components.map((c, i) => (
              <div key={i} className="flex justify-between gap-3">
                <span className="text-theme-text-primary">{c.name}</span>
                <span className="font-mono text-theme-text-secondary">{c.tag || '-'}</span>
              </div>
            ))}
          </div>
        </Section>
      )}
      {spec.cluster && (
        <Section title="Cluster" icon={Server}>
          <PropertyList>
            <Property label="Type" value={spec.cluster.type} />
            <Property label="Multitenant" value={yesNo(spec.cluster.multitenant)} />
            <Property label="Network policy" value={yesNo(spec.cluster.networkPolicy)} />
            <Property label="Domain" value={spec.cluster.domain} />
          </PropertyList>
        </Section>
      )}
      {spec.storage && (
        <Section title="Storage">
          <PropertyList>
            <Property label="Class" value={spec.storage.class} />
            <Property label="Size" value={spec.storage.size} />
          </PropertyList>
        </Section>
      )}
      {history.length > 0 && (
        <Section title="History" icon={History}>
          <div className="space-y-1 text-sm">
            {history.map((h, i) => (
              <div key={i} className="flex flex-wrap items-baseline gap-x-3 text-theme-text-secondary">
                <span className="font-mono text-theme-text-primary">{h.metadata?.flux || String(h.digest || '').slice(0, 19)}</span>
                <span>{h.lastReconciledStatus === 'ReconciliationSucceeded' ? 'Applied' : h.lastReconciledStatus}</span>
                {h.lastReconciled && <span className="text-theme-text-tertiary">{formatRelativeAgeTime(h.lastReconciled)}</span>}
                {typeof h.totalReconciliations === 'number' && <span className="text-theme-text-tertiary">{h.totalReconciliations} runs</span>}
              </div>
            ))}
          </div>
        </Section>
      )}
      <Inventory entries={status.inventory?.entries} />
      <ConditionsSection conditions={status.conditions} />
    </>
  )
}

export function FluxReportRenderer({ data }: Props) {
  const spec = data.spec || {}
  const components: any[] = spec.components || []
  const reconcilers: any[] = spec.reconcilers || []
  return (
    <>
      <Section title="Distribution" icon={Layers}>
        <PropertyList>
          <Property label="Flux" value={spec.distribution?.version} />
          <Property label="Status" value={spec.distribution?.status} />
          <Property label="Managed by" value={spec.distribution?.managedBy} />
          <Property label="Operator" value={spec.operator?.version} />
        </PropertyList>
      </Section>
      {spec.cluster && (
        <Section title="Cluster" icon={Server}>
          <PropertyList>
            <Property label="Server" value={spec.cluster.serverVersion} />
            <Property label="Platform" value={spec.cluster.platform} />
            <Property label="Nodes" value={spec.cluster.nodes !== undefined ? String(spec.cluster.nodes) : undefined} />
          </PropertyList>
        </Section>
      )}
      {components.length > 0 && (
        <Section title={`Components (${components.length})`} icon={Boxes}>
          <table className="w-full text-sm">
            <tbody>
              {components.map((c, i) => (
                <tr key={i} className="border-b border-theme-border/60 last:border-0">
                  <td className="py-1 pr-3 text-theme-text-primary">{c.name}</td>
                  <td className="py-1 pr-3 font-mono text-theme-text-secondary">{(c.image && imageTag(c.image)) || '-'}</td>
                  <td className="py-1 text-theme-text-secondary">{c.ready ? 'Ready' : c.status || 'Not ready'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}
      <Section title={`Reconcilers (${reconcilers.length})`} icon={Workflow}>
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs text-theme-text-tertiary">
              <th className="py-1 pr-3 font-medium">Kind</th>
              <th className="py-1 pr-3 font-medium">Running</th>
              <th className="py-1 pr-3 font-medium">Failing</th>
              <th className="py-1 pr-3 font-medium">Suspended</th>
              <th className="py-1 font-medium">Size</th>
            </tr>
          </thead>
          <tbody>
            {reconcilers.map((r, i) => (
              <tr key={i} className="border-t border-theme-border/60">
                <td className="py-1 pr-3 text-theme-text-primary">{r.kind}</td>
                <td className="py-1 pr-3 text-theme-text-secondary">{r.stats?.running ?? '-'}</td>
                <td className="py-1 pr-3 text-theme-text-secondary">{r.stats?.failing ?? '-'}</td>
                <td className="py-1 pr-3 text-theme-text-secondary">{r.stats?.suspended ?? '-'}</td>
                <td className="py-1 text-theme-text-secondary">{r.stats?.totalSize || '-'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
      <ConditionsSection conditions={data.status?.conditions} />
    </>
  )
}

export function ResourceSetRenderer({ data, onNavigate }: Props) {
  const spec = data.spec || {}
  const namespace = data.metadata?.namespace || ''
  const inputs: any[] = spec.inputs || []
  const inputsFrom: any[] = spec.inputsFrom || []
  const dependsOn: any[] = spec.dependsOn || []
  return (
    <>
      <OperatorStatus data={data} />
      <Section title="Inputs">
        <div className="space-y-1 text-sm text-theme-text-secondary">
          <div>{inputs.length} inline</div>
          {inputsFrom.map((p, i) => (
            <div key={i}>
              <span className="text-theme-text-tertiary">{p.kind || 'ResourceSetInputProvider'} </span>
              {p.name ? (
                <ResourceLink name={p.name} kind="resourcesetinputproviders" group="fluxcd.controlplane.io" namespace={p.namespace || namespace} onNavigate={onNavigate} />
              ) : (
                <span>selector</span>
              )}
            </div>
          ))}
        </div>
      </Section>
      {dependsOn.length > 0 && (
        <Section title={`Depends on (${dependsOn.length})`}>
          <div className="space-y-1 text-sm text-theme-text-secondary">
            {dependsOn.map((d, i) => (
              <div key={i}>
                <span className="text-theme-text-tertiary">{d.kind} </span>
                <span className="text-theme-text-primary">{d.namespace ? `${d.namespace}/` : ''}{d.name}</span>
              </div>
            ))}
          </div>
        </Section>
      )}
      <Inventory entries={data.status?.inventory?.entries} />
      <ConditionsSection conditions={data.status?.conditions} />
    </>
  )
}

export function ResourceSetInputProviderRenderer({ data }: Props) {
  const spec = data.spec || {}
  return (
    <>
      <OperatorStatus data={data} />
      <Section title="Provider">
        <PropertyList>
          <Property label="Type" value={spec.type} />
          <Property label="URL" value={spec.url ? redactUrlCredentials(spec.url) : undefined} />
        </PropertyList>
      </Section>
      <ConditionsSection conditions={data.status?.conditions} />
    </>
  )
}
