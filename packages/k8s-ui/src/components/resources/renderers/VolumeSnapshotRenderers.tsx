import { Camera, Settings } from 'lucide-react'
import type { ResourceRef } from '../../../types'
import { Badge } from '../../ui/Badge'
import { Tooltip } from '../../ui/Tooltip'
import { healthToSeverity } from '../../../utils/badge-colors'
import { formatRelativeAgeTime } from '../../../utils/format'
import { Section, Property, PropertyList, ResourceLink, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import {
  VOLUME_SNAPSHOT_GROUP,
  getVolumeSnapshotStatus,
  getVolumeSnapshotErrorMessage,
  getVolumeSnapshotSource,
  getVolumeSnapshotRestoreSize,
  getVolumeSnapshotCreationTime,
  getVolumeSnapshotContentRef,
  isDefaultVolumeSnapshotClass,
} from '../resource-utils-volumesnapshot'

const MAX_PARAMETERS = 20

type Navigate = (ref: ResourceRef) => void

function Timestamp({ at }: { at?: string }) {
  if (!at) return <>Not reported</>
  return <Tooltip content={new Date(at).toLocaleString()}>{formatRelativeAgeTime(at)}</Tooltip>
}

function ReadyToUse({ value }: { value: unknown }) {
  return <>{value === true ? 'Yes' : value === false ? 'No' : 'Not reported'}</>
}

function deletionPolicyNote(policy: unknown): string | undefined {
  if (policy === 'Delete') return 'Deleting the VolumeSnapshot also deletes this content and the snapshot on the storage system.'
  if (policy === 'Retain') return 'The snapshot on the storage system and this content remain after the VolumeSnapshot is deleted.'
  return undefined
}

function ClassLink({ name, onNavigate }: { name?: string; onNavigate?: Navigate }) {
  return name
    ? <ResourceLink kind="VolumeSnapshotClass" group={VOLUME_SNAPSHOT_GROUP} name={name} onNavigate={onNavigate} />
    : <>Not specified</>
}

function ErrorBanner({ data }: { data: any }) {
  const message = getVolumeSnapshotErrorMessage(data)
  if (!message || data.status?.readyToUse === true) return null
  const at = typeof data.status?.error?.time === 'string' ? data.status.error.time : undefined
  return <AlertBanner
    variant="warning"
    title={at ? `Snapshot error, ${formatRelativeAgeTime(at)}` : 'Snapshot error'}
    message={<><span className="break-words">{message}</span><span className="mt-1 block text-xs">The controller keeps retrying and clears the error once the snapshot is ready.</span></>}
  />
}

const READY_NOTE = 'Ready to use is what the CSI driver reports: a volume can be restored from the snapshot. It does not establish application consistency or that a restore was tested.'

export function VolumeSnapshotRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const operationalIssuesShown = useOperationalIssuesShown()
  const state = getVolumeSnapshotStatus(data)
  const status = data.status ?? {}
  const namespace = data.metadata?.namespace ?? ''
  const source = getVolumeSnapshotSource(data)
  const bound = typeof status.boundVolumeSnapshotContentName === 'string' && status.boundVolumeSnapshotContentName ? status.boundVolumeSnapshotContentName : undefined
  const size = getVolumeSnapshotRestoreSize(data)
  return <>
    {!operationalIssuesShown && <ErrorBanner data={data} />}
    {state.text === 'Not ready' && <AlertBanner variant="info" title="Not ready to use yet" message="The driver reports the snapshot as not ready, for example while it is still being uploaded. A volume restored from it waits until it is ready." />}
    <Section title="Volume Snapshot" icon={Camera} defaultExpanded>
      <PropertyList>
        <Property label="Status" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="Ready to use" value={<ReadyToUse value={status.readyToUse} />} />
        <Property label={source?.kind === 'VolumeSnapshotContent' ? 'Source (pre-provisioned)' : 'Source PVC'} value={
          !source ? 'Not specified'
            : source.kind === 'PersistentVolumeClaim'
              ? <ResourceLink kind="persistentvolumeclaims" name={source.name} namespace={namespace} onNavigate={onNavigate} />
              : <ResourceLink kind="VolumeSnapshotContent" group={VOLUME_SNAPSHOT_GROUP} name={source.name} onNavigate={onNavigate} />
        } />
        <Property label="Snapshot class" value={<ClassLink name={data.spec?.volumeSnapshotClassName} onNavigate={onNavigate} />} />
        <Property label="Bound content" value={bound
          ? <ResourceLink kind="VolumeSnapshotContent" group={VOLUME_SNAPSHOT_GROUP} name={bound} onNavigate={onNavigate} />
          : 'Not bound yet'} />
        <Property label="Restore size" value={size?.text ?? 'Not reported'} />
        <Property label="Snapshot taken" value={<Timestamp at={getVolumeSnapshotCreationTime(data)} />} />
        {typeof status.volumeGroupSnapshotName === 'string' && <Property label="Group snapshot" value={status.volumeGroupSnapshotName} />}
      </PropertyList>
      <p className="mt-3 text-xs text-theme-text-tertiary">{READY_NOTE}</p>
    </Section>
  </>
}

export function VolumeSnapshotContentRenderer({ data, onNavigate }: { data: any; onNavigate?: Navigate }) {
  const state = getVolumeSnapshotStatus(data)
  const spec = data.spec ?? {}
  const status = data.status ?? {}
  const ref = getVolumeSnapshotContentRef(data)
  const size = getVolumeSnapshotRestoreSize(data)
  const volumeHandle = typeof spec.source?.volumeHandle === 'string' ? spec.source.volumeHandle : undefined
  const sourceSnapshotHandle = typeof spec.source?.snapshotHandle === 'string' ? spec.source.snapshotHandle : undefined
  return <>
    <ErrorBanner data={data} />
    <Section title="Volume Snapshot Content" icon={Camera} defaultExpanded>
      <PropertyList>
        <Property label="Status" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="Ready to use" value={<ReadyToUse value={status.readyToUse} />} />
        <Property label="Volume snapshot" value={ref
          ? <span><ResourceLink kind="VolumeSnapshot" group={VOLUME_SNAPSHOT_GROUP} name={ref.name} namespace={ref.namespace} onNavigate={onNavigate} />{!ref.bound && <span className="text-theme-text-secondary"> (not bound yet)</span>}</span>
          : 'Not specified'} />
        <Property label="Driver" value={spec.driver ?? 'Not specified'} />
        <Property label="Deletion policy" value={spec.deletionPolicy ?? 'Not specified'} />
        <Property label="Snapshot class" value={<ClassLink name={spec.volumeSnapshotClassName} onNavigate={onNavigate} />} />
        <Property label={volumeHandle ? 'Source volume handle' : 'Pre-provisioned handle'} value={volumeHandle ?? sourceSnapshotHandle} />
        <Property label="Snapshot handle" value={status.snapshotHandle ?? 'Not reported'} />
        <Property label="Source volume mode" value={spec.sourceVolumeMode} />
        <Property label="Restore size" value={size?.text ?? 'Not reported'} />
        <Property label="Snapshot taken" value={<Timestamp at={getVolumeSnapshotCreationTime(data)} />} />
      </PropertyList>
      {deletionPolicyNote(spec.deletionPolicy) && <p className="mt-3 text-xs text-theme-text-tertiary">{deletionPolicyNote(spec.deletionPolicy)}</p>}
      <p className="mt-2 text-xs text-theme-text-tertiary">{READY_NOTE}</p>
    </Section>
  </>
}

export function VolumeSnapshotClassRenderer({ data }: { data: any }) {
  const parameters = data.parameters && typeof data.parameters === 'object' ? Object.entries(data.parameters) : []
  return <>
    <Section title="Volume Snapshot Class" icon={Camera} defaultExpanded>
      <PropertyList>
        <Property label="Driver" value={data.driver ?? 'Not specified'} />
        <Property label="Deletion policy" value={data.deletionPolicy ?? 'Not specified'} />
        <Property label="Default class" value={isDefaultVolumeSnapshotClass(data) ? 'Yes, for snapshots of this driver that name no class' : 'No'} />
      </PropertyList>
      {deletionPolicyNote(data.deletionPolicy) && <p className="mt-3 text-xs text-theme-text-tertiary">Applies to contents created from this class. {deletionPolicyNote(data.deletionPolicy)}</p>}
    </Section>
    {parameters.length > 0 && <Section title={`Parameters (${parameters.length})`} icon={Settings} defaultExpanded>
      <PropertyList>
        {parameters.slice(0, MAX_PARAMETERS).map(([key, value]) => <Property key={key} label={key} value={String(value)} />)}
      </PropertyList>
      {parameters.length > MAX_PARAMETERS && <p className="mt-2 text-xs text-theme-text-secondary">Showing {MAX_PARAMETERS} of {parameters.length} parameters.</p>}
    </Section>}
  </>
}
