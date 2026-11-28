import { HeartPulse, Server, HardDrive, Database, Copy, Repeat, Settings } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import { Tooltip } from '../../ui/Tooltip'
import { healthToSeverity } from '../../../utils/badge-colors'
import { formatRelativeAgeTime } from '../../../utils/format'
import { Section, Property, PropertyList, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import {
  getCephClusterStatus,
  getCephHealth,
  getCephReconcileFailure,
  getCephCapacity,
  getCephOSDStores,
  getCephDeviceClasses,
  getCephStaleAfterMs,
  isCephCheckAhead,
  formatCephBytes,
  getCephBlockPoolStatus,
  getCephBlockPoolDeletionBlocked,
  getCephBlockPoolProtection,
  getCephBlockPoolFailureDomain,
} from '../resource-utils-rook'

const MAX_PARAMETERS = 20

function Timestamp({ at }: { at?: string }) {
  if (!at) return <>Not reported</>
  return <Tooltip content={new Date(at).toLocaleString()}>{formatRelativeAgeTime(at)}</Tooltip>
}

function yesNo(v: unknown): string | undefined {
  return v === true ? 'Yes' : v === false ? 'No' : undefined
}

function HealthBadge({ health }: { health: string }) {
  const severity = health === 'HEALTH_OK' ? 'success' : health === 'HEALTH_WARN' ? 'warning' : health === 'HEALTH_ERR' ? 'error' : 'neutral'
  return <Badge severity={severity}>{health}</Badge>
}

const CLUSTER_NOTE = 'Ceph health is what Rook last read from the ceph status command. Configured monitor counts and pool replica sizes describe what Ceph should run; they do not show that it does.'

export function CephClusterRenderer({ data }: { data: any }) {
  const operationalIssuesShown = useOperationalIssuesShown()
  const state = getCephClusterStatus(data)
  const health = getCephHealth(data)
  const failure = getCephReconcileFailure(data)
  const capacity = getCephCapacity(data)
  const osds = getCephOSDStores(data)
  const deviceClasses = getCephDeviceClasses(data)
  const spec = data.spec ?? {}
  const status = data.status ?? {}
  const staleAfterMinutes = Math.round(getCephStaleAfterMs(data) / 60_000)
  return <>
    {!operationalIssuesShown && failure && <AlertBanner
      variant="warning"
      title="Rook's last reconcile failed"
      message={<><span className="break-words">{failure.message || 'Rook reported no message.'}</span><span className="mt-1 block text-xs">Rook keeps retrying. Ceph itself can still be healthy.</span></>}
    />}
    {health.state === 'stale' && <AlertBanner
      variant="unknown"
      title="Ceph health is stale"
      message={isCephCheckAhead(health)
        ? `Rook stamped its last check ahead of this clock, so how long ago Ceph reported ${health.status} is unknown. Check the clocks on the Rook operator's node and this machine.`
        : `Rook has not refreshed the health in over ${staleAfterMinutes} minutes, so ${health.status} is what Ceph reported then, not now. Check that the Rook operator is running.`}
    />}
    {health.state === 'disabled' && <AlertBanner
      variant="unknown"
      title="Ceph health checks are disabled"
      message="spec.healthCheck.daemonHealth.status.disabled stops Rook from refreshing the health, so any health shown here is not current."
    />}
    <Section title="Ceph Health" icon={HeartPulse} defaultExpanded>
      <PropertyList>
        <Property label="Status" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="Reported health" value={health.status
          ? <span className="flex items-center gap-2"><HealthBadge health={health.status} />{health.state !== 'fresh' && <span className="text-theme-text-secondary">{health.state === 'stale' ? 'stale' : 'not current'}</span>}</span>
          : 'Not reported'} />
        <Property label="Last checked" value={<Timestamp at={health.lastChecked} />} />
        {health.lastChanged && <Property label="Changed" value={<span><Timestamp at={health.lastChanged} />{health.previous && <span className="text-theme-text-secondary"> (from {health.previous})</span>}</span>} />}
      </PropertyList>
      {health.checks.length > 0 && <div className="mt-3 space-y-2">
        <div className="text-xs font-medium text-theme-text-secondary">Health checks ({health.checks.length})</div>
        {health.checks.map(check => <div key={check.code} className="text-sm">
          <div className="flex items-center gap-2">
            <span className="font-mono text-theme-text-primary">{check.code}</span>
            {check.severity && <HealthBadge health={check.severity} />}
          </div>
          {check.message && <div className="break-words text-theme-text-secondary">{check.message}</div>}
        </div>)}
      </div>}
      <p className="mt-3 text-xs text-theme-text-tertiary">{CLUSTER_NOTE}</p>
    </Section>
    <Section title="Cluster" icon={Server} defaultExpanded>
      <PropertyList>
        <Property label="Rook phase" value={status.phase ?? 'Not reported'} />
        <Property label="Rook message" value={status.message} />
        <Property label="Ceph version" value={status.version?.version ?? 'Not reported'} />
        <Property label="Ceph image" value={spec.cephVersion?.image} />
        <Property label="FSID" value={status.ceph?.fsid} />
        <Property label="External" value={spec.external?.enable === true ? 'Yes, Rook connects to a Ceph cluster it does not manage' : undefined} />
        <Property label="Monitors" value={Number.isInteger(spec.mon?.count) ? `${spec.mon.count}${spec.mon.allowMultiplePerNode ? ' (multiple per node allowed)' : ''}` : undefined} />
        <Property label="Managers" value={Number.isInteger(spec.mgr?.count) ? String(spec.mgr.count) : undefined} />
        <Property label="Data dir host path" value={spec.dataDirHostPath} />
        <Property label="Network provider" value={spec.network?.provider} />
      </PropertyList>
      {Number.isInteger(status.observedGeneration) && Number.isInteger(data.metadata?.generation) && status.observedGeneration < data.metadata.generation &&
        <p className="mt-3 text-xs text-theme-text-tertiary">Rook has not finished reconciling the latest spec (generation {data.metadata.generation}, last completed {status.observedGeneration}).</p>}
    </Section>
    <Section title="Storage" icon={HardDrive} defaultExpanded>
      <PropertyList>
        <Property label="Raw capacity" value={capacity
          ? `${formatCephBytes(capacity.used)} used of ${formatCephBytes(capacity.total)} (${Math.round(capacity.used / capacity.total * 100)}%)`
          : 'Not reported'} />
        {capacity?.available !== undefined && <Property label="Available" value={formatCephBytes(capacity.available)} />}
        {capacity && <Property label="Capacity updated" value={<Timestamp at={capacity.lastUpdated} />} />}
        <Property label="OSDs" value={osds ? `${osds.total} (${osds.stores.map(([store, n]) => `${n} ${store}`).join(', ')})` : 'Not reported'} />
        <Property label="Device classes" value={deviceClasses.length > 0 ? deviceClasses.join(', ') : undefined} />
        <Property label="Use all nodes" value={yesNo(spec.storage?.useAllNodes)} />
        <Property label="Use all devices" value={yesNo(spec.storage?.useAllDevices)} />
        <Property label="Device filter" value={spec.storage?.deviceFilter} />
      </PropertyList>
      <p className="mt-3 text-xs text-theme-text-tertiary">Raw capacity counts every replica; usable space depends on each pool's data protection.</p>
    </Section>
  </>
}

const POOL_NOTE = 'Configured data protection and failure domain describe how Ceph should place data. They do not show whether placement groups are healthy now: that is in the CephCluster\'s Ceph health.'

export function CephBlockPoolRenderer({ data }: { data: any }) {
  const operationalIssuesShown = useOperationalIssuesShown()
  const state = getCephBlockPoolStatus(data)
  const spec = data.spec ?? {}
  const status = data.status ?? {}
  const protection = getCephBlockPoolProtection(data)
  const blocked = getCephBlockPoolDeletionBlocked(data)
  const parameters = spec.parameters && typeof spec.parameters === 'object' ? Object.entries(spec.parameters) : []
  const mirroring = spec.mirroring?.enabled === true ? spec.mirroring : undefined
  const mirroringSummary = status.mirroringStatus?.summary
  const quotas = spec.quotas ?? {}
  return <>
    {!operationalIssuesShown && state.text === 'Failure' && <AlertBanner
      variant="warning"
      title="Rook failed to configure this pool"
      message={<><span className="break-words">{status.message || 'Rook reported no message.'}</span><span className="mt-1 block text-xs">Rook keeps retrying; the operator log has the error.</span></>}
    />}
    {state.text === 'Deletion blocked' && <AlertBanner
      variant="warning"
      title="Deletion is blocked"
      items={blocked.filter(Boolean)}
      message={blocked.some(Boolean) ? undefined : 'Rook will not delete a pool that has dependents or still holds images.'}
    />}
    <Section title="Block Pool" icon={Database} defaultExpanded>
      <PropertyList>
        <Property label="Status" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="Rook message" value={status.message} />
        <Property label="Ceph pool name" value={spec.name || data.metadata?.name} />
        <Property label="Pool ID" value={Number.isInteger(status.poolID) && status.poolID > 0 ? String(status.poolID) : undefined} />
        <Property label="Application" value={spec.application} />
      </PropertyList>
      {Number.isInteger(status.observedGeneration) && Number.isInteger(data.metadata?.generation) && status.observedGeneration < data.metadata.generation &&
        <p className="mt-3 text-xs text-theme-text-tertiary">Rook has not applied the latest spec yet (generation {data.metadata.generation}, last applied {status.observedGeneration}).</p>}
    </Section>
    <Section title="Data Protection" icon={Copy} defaultExpanded>
      <PropertyList>
        <Property label="Protection" value={protection?.text ?? 'Not specified'} />
        {protection?.kind === 'replicated' && <>
          <Property label="Require safe replica size" value={yesNo(spec.replicated?.requireSafeReplicaSize)} />
          <Property label="Replicas per failure domain" value={Number.isInteger(spec.replicated?.replicasPerFailureDomain) && spec.replicated.replicasPerFailureDomain > 0 ? String(spec.replicated.replicasPerFailureDomain) : undefined} />
          <Property label="Sub failure domain" value={spec.replicated?.subFailureDomain} />
          <Property label="Hybrid storage" value={spec.replicated?.hybridStorage ? `Primary ${spec.replicated.hybridStorage.primaryDeviceClass}, secondary ${spec.replicated.hybridStorage.secondaryDeviceClass}` : undefined} />
        </>}
        {protection?.kind === 'erasureCoded' && <Property label="Algorithm" value={spec.erasureCoded?.algorithm} />}
        <Property label="Failure domain" value={getCephBlockPoolFailureDomain(data) ?? 'Not reported'} />
        <Property label="Device class" value={spec.deviceClass} />
        <Property label="CRUSH root" value={spec.crushRoot} />
        <Property label="Compression" value={spec.compressionMode} />
        <Property label="Target size ratio" value={typeof spec.replicated?.targetSizeRatio === 'number' && spec.replicated.targetSizeRatio > 0 ? String(spec.replicated.targetSizeRatio) : undefined} />
        <Property label="Quota (size)" value={quotas.maxSize ?? (typeof quotas.maxBytes === 'number' ? formatCephBytes(quotas.maxBytes) : undefined)} />
        <Property label="Quota (objects)" value={typeof quotas.maxObjects === 'number' ? String(quotas.maxObjects) : undefined} />
      </PropertyList>
      <p className="mt-3 text-xs text-theme-text-tertiary">{POOL_NOTE}</p>
    </Section>
    {mirroring && <Section title="Mirroring" icon={Repeat} defaultExpanded>
      <PropertyList>
        <Property label="Mode" value={mirroring.mode ?? 'Not specified'} />
        <Property label="Snapshot schedules" value={Array.isArray(mirroring.snapshotSchedules) && mirroring.snapshotSchedules.length > 0
          ? mirroring.snapshotSchedules.map((s: any) => [s?.interval, s?.startTime && `from ${s.startTime}`].filter(Boolean).join(' ')).filter(Boolean).join(', ')
          : undefined} />
        <Property label="Reported health" value={mirroringSummary?.health ?? 'Not reported'} />
        <Property label="Daemon health" value={mirroringSummary?.daemon_health} />
        <Property label="Image health" value={mirroringSummary?.image_health} />
        {status.mirroringStatus && <Property label="Last checked" value={<Timestamp at={status.mirroringStatus.lastChecked} />} />}
      </PropertyList>
    </Section>}
    {parameters.length > 0 && <Section title={`Parameters (${parameters.length})`} icon={Settings} defaultExpanded>
      <PropertyList>
        {parameters.slice(0, MAX_PARAMETERS).map(([key, value]) => <Property key={key} label={key} value={String(value)} />)}
      </PropertyList>
      {parameters.length > MAX_PARAMETERS && <p className="mt-2 text-xs text-theme-text-secondary">Showing {MAX_PARAMETERS} of {parameters.length} parameters.</p>}
    </Section>}
  </>
}
