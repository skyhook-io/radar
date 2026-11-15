import type { ResourceRef } from '../../../types'
import { Badge } from '../../ui/Badge'
import { healthToSeverity } from '../../../utils/badge-colors'
import { Section, Property, PropertyList, ResourceLink, ConditionsSection, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import {
  getStrimziClusterStatus,
  getStrimziKafkaListeners,
  getStrimziKafkaPoolNames,
  getStrimziPoolRoles,
  getStrimziPoolDrift,
  describeStrimziStorage,
  describeStrimziVersion,
  getStrimziWarningReasons,
} from '../resource-utils-strimzi'

const MAX_LISTED = 20
const GROUP = 'kafka.strimzi.io'

function sanitizedConditions(status: any) {
  return Array.isArray(status.conditions) ? status.conditions
    .filter((c: any) => typeof c?.type === 'string' && typeof c?.status === 'string')
    .map((c: any) => ({
      type: c.type,
      status: c.status,
      message: typeof c.message === 'string' && c.message.trim() ? 'Strimzi condition message withheld; inspect status.conditions in YAML.' : undefined,
      reason: typeof c.reason === 'string' ? c.reason : undefined,
      lastTransitionTime: typeof c.lastTransitionTime === 'string' ? c.lastTransitionTime : undefined,
    })) : []
}

function conditionTone(current: boolean) {
  return (c: any) => !current ? 'unknown' as const
    : c.type === 'NotReady' && c.status === 'True' ? 'fail' as const
    : c.type === 'Warning' && c.status === 'True' ? 'warning' as const
    : 'neutral' as const
}

function StatusNote({ children }: { children: string }) {
  return <p className="mb-3 text-xs text-theme-text-secondary">{children}</p>
}

function ObservedGeneration({ data }: { data: any }) {
  return <Property label="Observed / desired generation" value={`${data.status?.observedGeneration ?? 'Unknown'} / ${data.metadata?.generation ?? 'Unknown'}`} />
}

export function StrimziKafkaRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const operationalIssuesShown = useOperationalIssuesShown()
  const state = getStrimziClusterStatus(data)
  const spec = data.spec?.kafka ?? {}
  const status = data.status ?? {}
  const namespace = data.metadata?.namespace
  const listeners = getStrimziKafkaListeners(data)
  const pools = getStrimziKafkaPoolNames(data)
  const nodeIds: unknown[] = Array.isArray(status.registeredNodeIds) ? status.registeredNodeIds : []
  const storage = describeStrimziStorage(spec.storage)
  const zookeeper = data.spec?.zookeeper
  const warnings = getStrimziWarningReasons(data)
  return <>
    <Section title="Kafka Cluster" defaultExpanded>
      <StatusNote>{state.snapshotCurrent
        ? 'Strimzi operator snapshot of the last reconciliation. A reconciled resource does not establish that clients can connect.'
        : 'Reconciliation is paused, stale or unreported. These observations do not establish current state.'}</StatusNote>
      <PropertyList>
        <Property label="Operator status" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="Kafka version" value={describeStrimziVersion(status.kafkaVersion, spec.version)} />
        <Property label="Metadata version" value={describeStrimziVersion(status.kafkaMetadataVersion, spec.metadataVersion)} />
        {(typeof status.kafkaMetadataState === 'string' || zookeeper) && <Property label="Metadata state" value={typeof status.kafkaMetadataState === 'string' ? status.kafkaMetadataState : 'ZooKeeper (declared)'} />}
        <Property label="Cluster ID" value={<span className="break-all">{status.clusterId ?? 'Not reported'}</span>} />
        {spec.replicas !== undefined && <Property label="Broker replicas (spec.kafka)" value={spec.replicas} />}
        {zookeeper?.replicas !== undefined && <Property label="ZooKeeper replicas" value={zookeeper.replicas} />}
        {status.operatorLastSuccessfulVersion && <Property label="Last successful operator" value={status.operatorLastSuccessfulVersion} />}
        <ObservedGeneration data={data} />
      </PropertyList>
      {!operationalIssuesShown && state.level === 'degraded' && <AlertBanner variant="warning" title={state.text} message="Strimzi reports NotReady. Inspect the conditions below, the cluster operator logs and the broker Pods." />}
      {state.level === 'healthy' && warnings.length > 0 && <AlertBanner variant="warning" title="Strimzi reported warnings" message={`Reconciliation succeeded, but Strimzi flagged the configuration (${warnings.join(', ')}). Some warnings mean data loss on restart or an ignored change; inspect the conditions below.`} />}
    </Section>
    <Section title={`Node Pools (${pools.length})`} defaultExpanded>
      {pools.length === 0
        ? <p className="text-sm text-theme-text-secondary">{spec.replicas !== undefined
            ? 'No node pools reported. Brokers are declared on the Kafka resource (spec.kafka.replicas), which Strimzi supports only before 0.46.'
            : `No node pools reported. Strimzi leaves this list empty when a reconciliation fails before reaching the pools, and from 0.46 a Kafka needs at least one KafkaNodePool labelled strimzi.io/cluster=${data.metadata?.name ?? '<this Kafka>'}.`}</p>
        : <PropertyList>
            {pools.slice(0, MAX_LISTED).map(name => <Property key={name} label="KafkaNodePool" value={<ResourceLink kind="KafkaNodePool" group={GROUP} name={name} namespace={namespace} onNavigate={onNavigate} />} />)}
          </PropertyList>}
      {pools.length > MAX_LISTED && <p className="mt-2 text-xs text-theme-text-secondary">Showing {MAX_LISTED} of {pools.length} pools.</p>}
      {nodeIds.length > 0 && <p className="mt-2 text-xs text-theme-text-secondary break-all">Registered node IDs: {nodeIds.slice(0, 50).join(', ')}{nodeIds.length > 50 && `; ${nodeIds.length - 50} more in YAML`}</p>}
    </Section>
    <Section title={`Listeners (${listeners.length})`} defaultExpanded>
      {listeners.length === 0
        ? <p className="text-sm text-theme-text-secondary">No listeners declared or reported.</p>
        : <div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr className="text-theme-text-secondary"><th className="pr-3 pb-2">Name</th><th className="pr-3 pb-2">Type</th><th className="pr-3 pb-2">Port</th><th className="pr-3 pb-2">TLS</th><th className="pr-3 pb-2">Auth</th><th className="pb-2">Bootstrap address</th></tr></thead><tbody>
            {listeners.slice(0, MAX_LISTED).map(l => <tr key={l.name} className="border-t border-theme-border">
              <td className="py-2 pr-3">{l.name}</td>
              <td className="py-2 pr-3">{l.type ?? 'Not declared'}</td>
              <td className="py-2 pr-3">{l.port ?? 'Not declared'}</td>
              <td className="py-2 pr-3">{l.tls === undefined ? 'Not declared' : l.tls ? 'Yes' : 'No'}</td>
              <td className="py-2 pr-3">{!l.declared ? 'Not declared' : l.authentication ?? 'None'}</td>
              <td className="py-2 break-all">{l.bootstrapServers ?? (l.addresses.length ? l.addresses.slice(0, 5).join(', ') : 'Not reported')}</td>
            </tr>)}
          </tbody></table></div>}
      {listeners.length > MAX_LISTED && <p className="mt-2 text-xs text-theme-text-secondary">Showing {MAX_LISTED} of {listeners.length} listeners.</p>}
      <p className="mt-3 text-xs text-theme-text-tertiary">Addresses are reported by Strimzi; reachability from clients is not checked. Listener certificates are not shown.</p>
    </Section>
    {storage.length > 0 && <Section title="Broker Storage (spec.kafka)" defaultExpanded={false}>
      <ul className="text-sm space-y-1">{storage.slice(0, MAX_LISTED).map((line, i) => <li key={i}>{line}</li>)}</ul>
    </Section>}
    <ConditionsSection conditions={sanitizedConditions(status)} getConditionTone={conditionTone(state.snapshotCurrent)} />
  </>
}

export function StrimziKafkaNodePoolRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const state = getStrimziClusterStatus(data)
  const spec = data.spec ?? {}
  const status = data.status ?? {}
  const cluster = data.metadata?.labels?.['strimzi.io/cluster']
  const roles = getStrimziPoolRoles(data)
  const drift = getStrimziPoolDrift(data)
  const nodeIds: unknown[] = Array.isArray(status.nodeIds) ? status.nodeIds : []
  const storage = describeStrimziStorage(spec.storage)
  const requests = spec.resources?.requests ?? {}
  const limits = spec.resources?.limits ?? {}
  return <>
    <Section title="Node Pool" defaultExpanded>
      <StatusNote>{state.snapshotCurrent
        ? 'Strimzi wrote this status for the pool\'s current spec and assigned its node IDs. Broker health is reported on the Kafka resource, not on the pool.'
        : 'Strimzi has not written status for this pool\'s current spec. It does so only while reconciling the Kafka named in the strimzi.io/cluster label.'}</StatusNote>
      <PropertyList>
        <Property label="Operator status" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="Kafka" value={cluster ? <ResourceLink kind="Kafka" group={GROUP} name={cluster} namespace={data.metadata?.namespace} onNavigate={onNavigate} /> : 'Not specified (strimzi.io/cluster label missing)'} />
        <Property label="Roles" value={`${roles.length ? roles.join(', ') : 'Not reported'}${drift.roles && drift.declaredRoles ? ` (declared ${drift.declaredRoles.join(', ') || 'none'})` : ''}`} />
        <Property label="Replicas (assigned / desired)" value={`${status.replicas ?? 'Unknown'} / ${spec.replicas ?? 'Unknown'}`} />
        <Property label="CPU request / limit" value={`${requests.cpu ?? 'Not set'} / ${limits.cpu ?? 'Not set'}`} />
        <Property label="Memory request / limit" value={`${requests.memory ?? 'Not set'} / ${limits.memory ?? 'Not set'}`} />
        <ObservedGeneration data={data} />
      </PropertyList>
      {state.text === 'Not applied' && <AlertBanner variant="warning" title="Strimzi did not apply this change" message="Strimzi kept the previous replicas or roles, usually because brokers that would be removed still hold partitions. The Kafka resource carries the reason as a ScaleDownPreventionCheck warning." />}
      {state.text === 'Warnings' && <AlertBanner variant="warning" title="Strimzi reported a warning" message="Part of this pool's spec may not have been applied. Inspect the conditions below and the cluster operator logs." />}
      <p className="mt-3 text-xs text-theme-text-tertiary">Assigned counts the node IDs Strimzi allocated for the pool, not the broker Pods that are running.</p>
    </Section>
    <Section title={`Node IDs (${nodeIds.length})`} defaultExpanded={nodeIds.length > 0}>
      {nodeIds.length === 0
        ? <p className="text-sm text-theme-text-secondary">No node IDs reported.</p>
        : <p className="text-sm break-all">{nodeIds.slice(0, 50).join(', ')}{nodeIds.length > 50 && `; ${nodeIds.length - 50} more in YAML`}</p>}
    </Section>
    {storage.length > 0 && <Section title="Storage (spec)" defaultExpanded>
      <ul className="text-sm space-y-1">{storage.slice(0, MAX_LISTED).map((line, i) => <li key={i}>{line}</li>)}</ul>
    </Section>}
    <ConditionsSection conditions={sanitizedConditions(status)} getConditionTone={conditionTone(state.snapshotCurrent)} />
  </>
}
