import { clsx } from 'clsx'
import {
  CNPGClusterHASection,
  PaneLoader,
  RefLink,
  StatusDot,
  cnpgHASlotInstance,
  formatAge,
  formatGrant,
  toneTextClass,
  type CNPGClusterHA,
  type NavigateToRef,
} from '@skyhook-io/k8s-ui'
import { useCNPGRuntime, type CNPGRuntimeInstance } from '../../api/cnpg'
import { cnpgInstanceLive, cnpgInstanceLiveUnavailable, useCNPGClusterHA } from '../../api/cnpg-ha'
import { CNPGReplicationView } from './CNPGReplicationView'
import { CNPGTabVerdict } from './CNPGClusterTabs'
import { formatBytes } from './lsn'
import { useCNPGFleet } from './useCNPGSidebarWorkspace'
import { Card, ProxyDenied, SourceState } from './runtimeParts'
import { cnpgRuntimeMeasured } from './runtimeModel'
import { buildWorkloadPath } from '../../utils/navigation'
import { useCNPGNavigate } from './useCNPGNavigate'
import { Notice, RefreshFailedNotice } from '../workspace/layout'

/**
 * Instances, replication and HA: every instance with what Kubernetes says
 * about its Pod and what PostgreSQL reports about its role, each standby's
 * streaming state and the slot the primary keeps for it, then the slots no
 * instance accounts for and the HA configuration a switchover or failover meets.
 */
export function CNPGReplicationTab({
  namespace,
  name,
  onOpenLogs,
  onOpenHistory,
  onNavigate,
}: {
  namespace: string
  name: string
  onOpenLogs?: (pod: string) => void
  onOpenHistory?: () => void
  onNavigate?: NavigateToRef
}) {
  const navigate = useCNPGNavigate()
  const runtime = useCNPGRuntime(namespace, name)
  const ha = useCNPGClusterHA(namespace, name)
  const { fleet } = useCNPGFleet([namespace])
  const clusterObject = fleet?.rows.find((r) => r.namespace === namespace && r.name === name)?.cluster
  const data = runtime.data
  const denied = data?.permission.proxy === 'denied'
  const grant = formatGrant(data?.permission.grant) ?? `get pods/proxy in namespace ${namespace}`
  const primary = data?.instances.find((i) => i.role === 'primary')
  const replicas = data?.instances.filter((i) => i.role !== 'primary') ?? []

  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-sm font-semibold text-theme-text-primary">Instances, replication and HA</h2>
        {data && (
          <span className="text-xs text-theme-text-tertiary">
            {denied ? 'Live instance data needs access you do not have' : cnpgRuntimeMeasured(data.instances, 'status') ? `Live instance data · sampled ${formatAge(data.sampledAt)} ago` : `No instance data yet; checked ${formatAge(data.sampledAt)} ago`}
          </span>
        )}
        {onOpenHistory && (
          <button type="button" onClick={onOpenHistory} className="ml-auto text-xs text-accent-text hover:underline">
            Replication history →
          </button>
        )}
      </div>
      <CNPGTabVerdict namespace={namespace} name={name} id="serving" />
      <CNPGTabVerdict namespace={namespace} name={name} id="replication" />
      <RefreshFailedNotice queries={[runtime, ha]} />

      {!data && runtime.isLoading ? (
        <PaneLoader label="Reading live state…" className="h-32" />
      ) : !data ? (
        <Notice>Live instance data could not be loaded: {runtime.error instanceof Error ? runtime.error.message : 'unknown error'}</Notice>
      ) : denied ? (
        <>
          <PodsOnly ha={ha.data} namespace={namespace} onNavigate={onNavigate} />
          <ProxyDenied what="Streaming state, WAL positions, backlog and replication slots" grant={grant} />
        </>
      ) : (
        <>
          <CNPGReplicationView
            namespace={namespace}
            cluster={name}
            primary={primary}
            replicas={replicas}
            onOpenLogs={onOpenLogs}
            ha={ha.data}
            clusterObject={clusterObject}
          />
          <OtherSlots
            primary={primary}
            instances={data.instances.map((i) => i.pod)}
            expectedInstances={[...(clusterObject?.status?.instanceNames ?? []), ...(ha.data?.expectedInstances ?? [])]}
            joiningInstances={ha.data?.jobs.items.filter((j) => j.role === 'join' && ['pending', 'active', 'running'].includes(j.phase)).map((j) => j.instance).filter((n): n is string => !!n)}
            clusterObject={clusterObject}
          />
        </>
      )}

      <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
        <CNPGClusterHASection
          ha={ha.data}
          live={cnpgInstanceLive(data)}
          liveUnavailable={cnpgInstanceLiveUnavailable(data, runtime.error)}
          loading={ha.isLoading}
          error={ha.error instanceof Error ? ha.error.message : undefined}
          onNavigate={onNavigate}
          title="High availability"
          currentPrimary={clusterObject?.status?.currentPrimary}
          hibernated={fleet?.rows.find((r) => r.namespace === namespace && r.name === name)?.hibernated}
          onOpenReachability={(svc) => navigate(buildWorkloadPath({ kind: 'services', group: '', namespace: svc.namespace, name: svc.name, tab: 'reachability' }))}
          showInstances={false}
          showCertificates={false}
        />
      </div>
    </div>
  )
}

/** Without pods/proxy: the instances as their Pods report them, never a streaming state. */
function PodsOnly({ ha, namespace, onNavigate }: { ha?: CNPGClusterHA; namespace: string; onNavigate?: NavigateToRef }) {
  return (
    <Card title="Instances" footer="Role from the Pod's cnpg.io/instanceRole label and readiness from Kubernetes. Whether each standby is streaming is read from PostgreSQL, which needs the access named below.">
      {!ha ? (
        <div className="text-sm text-theme-text-tertiary">Reading instance Pods…</div>
      ) : ha.pods.state !== 'ok' ? (
        <div className="text-sm text-theme-text-tertiary">Instance Pods are not readable{ha.pods.grant ? `: needs ${formatGrant(ha.pods.grant)}` : ''}.</div>
      ) : (
        <div className="space-y-1.5">
          {ha.instances.map((i) => (
            <div key={i.pod} className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-sm">
              <StatusDot tone={i.ready ? 'healthy' : 'unhealthy'} />
              <RefLink refTo={{ kind: 'Pod', group: '', namespace, name: i.pod }} onNavigate={onNavigate} mono />
              <span className="text-theme-text-secondary">{i.role === 'replica' ? 'standby' : i.role === 'unknown' ? 'role unknown' : 'primary'}</span>
              <span className={clsx('text-xs', i.ready ? 'text-theme-text-secondary' : toneTextClass('unhealthy'))}>Pod {i.ready ? 'ready' : 'not ready'}</span>
              {i.node && <span className="text-xs text-theme-text-tertiary">node {i.node}</span>}
              <span className="text-xs text-theme-text-tertiary">streaming not read</span>
            </div>
          ))}
        </div>
      )}
    </Card>
  )
}

export function OtherSlots({ primary, instances, expectedInstances = [], joiningInstances = [], clusterObject }: { primary?: CNPGRuntimeInstance; instances: string[]; expectedInstances?: string[]; joiningInstances?: string[]; clusterObject?: any }) {
  const readable = primary?.status.state === 'ok' || primary?.status.state === 'partial'
  if (!primary) return null
  if (!readable || !primary.status.slots) {
    return (
      <Card title="Other replication slots">
        <SourceState label="Slots" state={readable ? 'partial' : primary.status.state} error={primary.status.error ?? primary.status.reason ?? 'slots were not read'} />
      </Card>
    )
  }
  const standbys = instances.filter((name) => name !== primary.pod)
  const expectedStandbys = expectedInstances.filter((name) => name !== primary.pod)
  const allInstances = [...new Set([...standbys, ...expectedStandbys])]
  const waiting = primary.status.slots.filter((sl) => sl.type === 'physical' && !cnpgHASlotInstance(clusterObject, sl.name, standbys) && cnpgHASlotInstance(clusterObject, sl.name, expectedStandbys))
  const others = primary.status.slots.filter((sl) => sl.type !== 'physical' || !cnpgHASlotInstance(clusterObject, sl.name, allInstances))
  return (
    <>
    {waiting.length > 0 && (
      <Card title="Expected standby slots">
        {waiting.map((sl) => {
          const instance = cnpgHASlotInstance(clusterObject, sl.name, expectedStandbys)!
          return (
            <div key={sl.name} className={clsx('text-sm', sl.active === false ? toneTextClass('degraded') : 'text-theme-text-secondary')}>
              <span className="font-mono">{instance}</span> — {joiningInstances.includes(instance) ? 'waiting to join' : 'no instance Pod observed'}
              <div className="text-xs text-theme-text-tertiary">Slot {sl.name} · {sl.active === undefined ? 'state unknown' : sl.active ? 'active' : 'inactive'} · {sl.retainedBytes === undefined ? 'retained WAL not reported' : formatBytes(sl.retainedBytes)}</div>
            </div>
          )
        })}
      </Card>
    )}
    <Card title="Other replication slots" footer="Slot inventory from the primary’s instance manager. Physical slot associations use CloudNativePG’s naming convention.">
      {others.length === 0 ? (
        <div className="text-sm text-theme-text-tertiary">{primary.status.slots.length === 0 ? primary.status.slotsTruncated ? 'No slots in the reported inventory; inventory incomplete' : `No replication slots on ${primary.pod}` : primary.status.slots.length === 1 ? `The ${primary.status.slotsTruncated ? 'one reported' : 'only'} slot belongs to a standby` : `All ${primary.status.slots.length}${primary.status.slotsTruncated ? ' reported' : ''} slots belong to standbys`}</div>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-left text-[11px] uppercase tracking-wide text-theme-text-tertiary">
            <tr>
              <th className="py-1.5 pr-3">Slot</th>
              <th className="pr-3">Type</th>
              <th className="pr-3">Database</th>
              <th className="pr-3">State</th>
              <th className="pr-3">WAL status</th>
              <th className="text-right">Holds</th>
            </tr>
          </thead>
          <tbody className="table-divide-subtle">
            {others.map((sl) => (
              <tr key={sl.name}>
                <td className="py-1.5 pr-3 font-mono text-xs">{sl.name}{sl.type === 'physical' && <div className="font-sans text-theme-text-tertiary">association unverified</div>}</td>
                <td className="pr-3">{sl.type ?? '—'}</td>
                <td className="pr-3 font-mono text-xs">{sl.database ?? '—'}</td>
                <td className={clsx('pr-3', sl.active === false && toneTextClass('degraded'))}>{sl.active === undefined ? 'state unknown' : sl.active ? 'active' : 'inactive'}</td>
                <td className="pr-3">{sl.walStatus ?? '—'}</td>
                <td className="text-right font-mono">{formatBytes(sl.retainedBytes)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
    </>
  )
}
