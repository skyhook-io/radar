import { HardDrive, HeartPulse, Archive, History } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import { formatBytes } from '../../../utils/format'
import { formatDuration } from '../resource-utils'
import { TimeValue } from '../../ui/ScheduleValue'
import { longhornHealthyReplicas, longhornRebuilds, longhornReplicaUnschedulable, longhornVolumeConditionTone, longhornVolumeHealth } from '../resource-utils-longhorn'

interface Props {
  data: any
  /** The Volume's Engine, when the host could read it: healthy replicas and rebuild progress come only from it. */
  engine?: any
  /** The host tried to read the Engine and couldn't (or couldn't tell which one is current). */
  engineUnknown?: boolean
  /** The host finished reading Engines, so a missing engine means this volume has none. */
  engineLookedUp?: boolean
  /** Whether the caller can read a namespace; Longhorn's recorded names show only for those. Omitted: none show. */
  canReadNamespace?: (namespace: string) => boolean
}

function bytes(value: unknown): string | undefined {
  const n = Number(value)
  return Number.isFinite(n) && n > 0 ? formatBytes(n) : undefined
}

function since(timestamp?: string): string | undefined {
  const at = timestamp ? Date.parse(timestamp) : NaN
  return Number.isFinite(at) && Date.now() >= at ? formatDuration(Date.now() - at) : undefined
}

function lastSeen(timestamp?: string): string {
  if (!timestamp) return "still referenced at Longhorn's last update"
  const ago = since(timestamp)
  return ago ? `last seen ${ago} ago` : `last seen ${timestamp}`
}

// Names in status.kubernetesStatus are what Longhorn last recorded, not proof
// of current use (that is the PV under related resources), so they read as
// history and never link.
function LastSeenSection({ status, canReadNamespace }: { status: any; canReadNamespace?: (namespace: string) => boolean }) {
  const k = status?.kubernetesStatus
  const ns: string = k?.namespace || ''
  if (!ns || !canReadNamespace?.(ns)) return null
  const pods: any[] = (k.workloadsStatus ?? []).filter((w: any) => w?.podName)
  if (!k.pvcName && pods.length === 0) return null
  return (
    <Section title="Last seen by Longhorn" icon={History} defaultExpanded>
      <PropertyList>
        {k.pvcName && <Property label="PVC" value={`${ns}/${k.pvcName} · ${lastSeen(k.lastPVCRefAt)}`} />}
        {pods.map((w) => (
          <Property key={w.podName} label="Pod" value={`${w.podName}${w.workloadType && w.workloadName ? ` (${w.workloadType} ${w.workloadName})` : ''} · ${lastSeen(k.lastPodRefAt)}`} />
        ))}
      </PropertyList>
    </Section>
  )
}

export function LonghornVolumeRenderer({ data, engine, engineUnknown, engineLookedUp, canReadNamespace }: Props) {
  const spec = data?.spec ?? {}
  const status = data?.status ?? {}
  const health = longhornVolumeHealth(data)
  const desired = Number(spec.numberOfReplicas) || undefined
  const healthy = longhornHealthyReplicas(engine)
  const rebuilds = longhornRebuilds(engine)
  const degradedFor = since(status.lastDegradedAt)
  const rebuilding = rebuilds.some((r) => !r.error)
  const unschedulable = longhornReplicaUnschedulable(data)
  // A stopped or absent Engine has no replica modes; say why the count is unknown.
  const engineIdle = healthy === undefined && !engineUnknown && (status.state === 'detached' || (!!engineLookedUp && !engine))

  return (
    <>
      {health === 'faulted' && (
        <AlertBanner variant="error" title="Faulted" message="No replica of this volume is usable. Longhorn can't serve its data until a replica is salvaged or the volume is restored from a backup." />
      )}
      {health === 'degraded' && (
        <AlertBanner
          variant="warning"
          title={`${rebuilding ? 'Rebuilding' : 'Degraded'}${degradedFor ? ` for ${degradedFor}` : ''}`}
          message={unschedulable
            ? "Fewer healthy replicas than requested, and Longhorn can't place a new one (its Scheduled condition is False). Check node disks and scheduling settings."
            : rebuilding
              ? 'Fewer healthy replicas than requested; Longhorn is rebuilding them, which is routine after a node drain or restart.'
              : 'Fewer healthy replicas than requested. Longhorn rebuilds them when it can place a replica; routine after a node drain or restart.'}
          items={rebuilds.map((r) => (r.error ? `${r.replica}: ${r.error}` : `${r.replica}: ${r.progress === undefined ? 'progress unknown' : `${r.progress}%`}`))}
        />
      )}

      <Section title="Volume" icon={HardDrive} defaultExpanded>
        <PropertyList>
          <Property label="Size" value={bytes(spec.size)} />
          <Property label="Actual size" value={bytes(status.actualSize)} />
          <Property label="Healthy replicas" value={`${healthy ?? '?'} of ${desired ?? '?'}${engineIdle ? ' (Engine not running)' : ''}`} />
          <Property label="Data locality" value={spec.dataLocality} />
          <Property label="Access mode" value={spec.accessMode} />
          <Property label="Frontend" value={spec.frontend} />
          <Property label="Data engine" value={spec.dataEngine} />
        </PropertyList>
      </Section>

      <Section title="Health" icon={HeartPulse} defaultExpanded>
        <PropertyList>
          <Property label="State" value={status.state} />
          <Property label="Robustness" value={status.robustness} />
          <Property label="Node" value={status.currentNodeID || undefined} />
          <Property label="Degraded since" value={status.lastDegradedAt ? <TimeValue timestamp={status.lastDegradedAt} /> : undefined} />
          <Property label="Last auto-salvaged" value={status.lastAutoSalvagedAt ? <TimeValue timestamp={status.lastAutoSalvagedAt} /> : undefined} />
        </PropertyList>
      </Section>

      <Section title="Backups" icon={Archive} defaultExpanded>
        <PropertyList>
          <Property label="Last backup" value={<TimeValue timestamp={status.lastBackupAt || undefined} />} />
        </PropertyList>
      </Section>

      <LastSeenSection status={status} canReadNamespace={canReadNamespace} />

      <ConditionsSection conditions={status.conditions} getConditionTone={longhornVolumeConditionTone} />
    </>
  )
}
