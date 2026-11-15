import { HardDrive, HeartPulse, Archive } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import { formatBytes } from '../../../utils/format'
import { formatDuration } from '../resource-utils'
import { longhornHealthyReplicas, longhornRebuilds, longhornVolumeHealth } from '../resource-utils-longhorn'

interface Props {
  data: any
  /** The Volume's Engine, when the host could read it: healthy replicas and rebuild progress come only from it. */
  engine?: any
}

function bytes(value: unknown): string | undefined {
  const n = Number(value)
  return Number.isFinite(n) && n > 0 ? formatBytes(n) : undefined
}

function since(timestamp?: string): string | undefined {
  const at = timestamp ? Date.parse(timestamp) : NaN
  return Number.isFinite(at) && Date.now() >= at ? formatDuration(Date.now() - at) : undefined
}

// Names in status.kubernetesStatus are what Longhorn last saw, not proof of
// current use, so this drawer shows none of them.
export function LonghornVolumeRenderer({ data, engine }: Props) {
  const spec = data?.spec ?? {}
  const status = data?.status ?? {}
  const health = longhornVolumeHealth(data)
  const desired = Number(spec.numberOfReplicas) || undefined
  const healthy = longhornHealthyReplicas(engine)
  const rebuilds = longhornRebuilds(engine)
  const degradedFor = since(status.lastDegradedAt)

  return (
    <>
      {health === 'faulted' && (
        <AlertBanner variant="error" title="Faulted" message="No replica of this volume is usable. Longhorn can't serve its data until a replica is salvaged or the volume is restored from a backup." />
      )}
      {health === 'degraded' && (
        <AlertBanner
          variant="warning"
          title={degradedFor ? `Rebuilding for ${degradedFor}` : 'Rebuilding'}
          message="Fewer healthy replicas than requested. Longhorn rebuilds them on its own when a node has room, which is routine after a node drain or restart."
          items={rebuilds.map((r) => (r.error ? `${r.replica}: ${r.error}` : `${r.replica}: ${r.progress}%`))}
        />
      )}

      <Section title="Volume" icon={HardDrive}>
        <PropertyList>
          <Property label="Size" value={bytes(spec.size)} />
          <Property label="Actual size" value={bytes(status.actualSize)} />
          <Property label="Healthy replicas" value={desired ? `${healthy ?? '?'} of ${desired}` : undefined} />
          <Property label="Data locality" value={spec.dataLocality} />
          <Property label="Access mode" value={spec.accessMode} />
          <Property label="Frontend" value={spec.frontend} />
          <Property label="Data engine" value={spec.dataEngine} />
        </PropertyList>
      </Section>

      <Section title="Health" icon={HeartPulse}>
        <PropertyList>
          <Property label="State" value={status.state} />
          <Property label="Robustness" value={status.robustness} />
          <Property label="Node" value={status.currentNodeID || undefined} />
          <Property label="Degraded since" value={status.lastDegradedAt || undefined} />
          <Property label="Last auto-salvaged" value={status.lastAutoSalvagedAt || undefined} />
        </PropertyList>
      </Section>

      <Section title="Backups" icon={Archive}>
        <PropertyList>
          <Property label="Last backup" value={status.lastBackupAt || undefined} />
        </PropertyList>
      </Section>

      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
