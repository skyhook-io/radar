import { OpenTelemetryCollectorRenderer as BaseRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/OpenTelemetryRenderers'
import { getWorkloadStatus } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import { Badge } from '@skyhook-io/k8s-ui/components/ui/Badge'
import { healthToSeverity } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { Property, PropertyList, ResourceLink, Section } from '@skyhook-io/k8s-ui/components/ui/drawer-components'
import { apiVersionToGroup } from '@skyhook-io/k8s-ui/utils/navigation'
import type { ResourceRef } from '@skyhook-io/k8s-ui'
import { useResource } from '../../../api/client'

function OwnedWorkload({ root, reference, onNavigate, active }: { root: any; reference: ResourceRef; onNavigate?: (ref: ResourceRef) => void; active: boolean }) {
  const kind = `${reference.kind.toLowerCase()}s`
  const query = useResource<any>(kind, reference.namespace, reference.name, reference.group, { enabled: active, refetchInterval: active ? 30000 : false })
  const workload = query.data
  const owned = root.metadata.uid && workload?.metadata?.namespace === root.metadata.namespace && workload.metadata.ownerReferences?.some((owner: any) => owner.controller === true && apiVersionToGroup(owner.apiVersion) === 'opentelemetry.io' && owner.kind === 'OpenTelemetryCollector' && owner.name === root.metadata.name && owner.uid === root.metadata.uid)
  const status = owned ? getWorkloadStatus(workload, kind) : undefined
  return <Property label={<ResourceLink {...reference} label={`${reference.kind} / ${reference.name}`} onNavigate={onNavigate} />} value={query.error ? 'Could not read workload; open it for details.' : !workload ? 'Reading current status…' : !owned ? 'Not currently controlled by this Collector; status not attributed.' : <Badge severity={healthToSeverity(status!.level)}>{status!.text}</Badge>} />
}

export function OpenTelemetryCollectorRenderer({ data, onNavigate, active = true }: { data: any; onNavigate?: (ref: ResourceRef) => void; active?: boolean }) {
  const root = useResource<any>('opentelemetrycollectors', data.metadata.namespace, data.metadata.name, 'opentelemetry.io', { enabled: active, refetchInterval: active ? 30000 : false })
  const coverage = root.relationships?.ownership
  const children = (root.relationships?.children ?? []).filter(ref => ref.group === 'apps' && ['Deployment', 'StatefulSet', 'DaemonSet'].includes(ref.kind))
  const runtimeGap = coverage?.children.notChecked?.some(gap => !gap.kind || gap.group === 'apps' && ['Deployment', 'StatefulSet', 'DaemonSet'].includes(gap.kind))
  const partial = !coverage || coverage.unavailableReason || runtimeGap || coverage.children.truncated
  return <BaseRenderer data={data} workloadEvidence={<Section title="Current Workloads" defaultExpanded>
    <p className="mb-2 text-xs text-theme-text-tertiary">Workloads this Collector runs. Ready Pods do not confirm that telemetry is delivered.</p>
    {root.isLoading ? <p className="text-sm text-theme-text-secondary">Reading owned workloads…</p> : root.error ? <p className="text-sm text-theme-text-secondary">Could not read owned workloads. Retry or inspect Related Resources.</p> : !coverage ? <p className="text-sm text-theme-text-secondary">Owned-workload evidence is unavailable. Related Resources may provide more detail.</p> : children.length ? <PropertyList>{children.slice(0, 3).map(ref => <OwnedWorkload key={`${ref.group}/${ref.kind}/${ref.namespace}/${ref.name}`} root={data} reference={ref} onNavigate={onNavigate} active={active} />)}</PropertyList> : <p className="text-sm text-theme-text-secondary">No standalone workloads observed{partial ? ' in the available evidence' : ''}. {data.spec?.mode === 'sidecar' ? 'Sidecar mode runs inside annotated application Pods.' : 'Inspect reconciliation and management state if you expected a runtime.'}</p>}
    {(partial || children.length > 3) && <p className="mt-2 text-xs text-theme-text-tertiary">This workload preview is incomplete. Inspect Related Resources for coverage and additional owned objects.</p>}
    {coverage?.children.narrowed && <p className="mt-2 text-xs text-theme-text-tertiary">Only resources you can read are shown.</p>}
  </Section>} />
}
