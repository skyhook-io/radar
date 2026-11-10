import { Database, Clock } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import {
  getBackupRepositoryStatus,
  getBackupRepositoryType,
  getBackupRepositoryVolumeNamespace,
  getBackupRepositoryLocation,
  getBackupRepositoryMaintenanceFrequency,
  getBackupRepositoryMessage,
} from '../resource-utils-velero'
import { VeleroPhaseValue } from './velero-cells'
import { TimeValue } from '../../ui/ScheduleValue'

interface VeleroBackupRepositoryRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

/**
 * A BackupRepository is where Velero's file-level backups of a namespace's
 * volumes are written. It raises its own issue when it goes `NotReady`, and
 * `status.message` is the only place the reason for that exists.
 */
export function VeleroBackupRepositoryRenderer({ data, onNavigate }: VeleroBackupRepositoryRendererProps) {
  const status = data.status || {}
  const repoStatus = getBackupRepositoryStatus(data)
  const message = getBackupRepositoryMessage(data)
  const volumeNamespace = getBackupRepositoryVolumeNamespace(data)
  const location = getBackupRepositoryLocation(data)

  return (
    <>
      {/* The reason, not just the state. A repository that is not ready means
          volume backups for its namespace are not being written, and Velero puts
          that consequence nowhere on the object. */}
      {repoStatus.level === 'unhealthy' && (
        <AlertBanner
          variant="error"
          title="Repository Not Ready"
          message={message
            ? message
            : `Velero reported this repository as not ready and gave no reason. Volume backups for ${volumeNamespace === '-' ? 'its namespace' : volumeNamespace} cannot be written while it stays that way.`}
        />
      )}

      <Section title="Status" icon={Clock} defaultExpanded>
        <PropertyList>
          <Property label="Phase" value={
            <VeleroPhaseValue status={repoStatus} phase={status.phase || ''} />
          } />
          {/* Velero runs maintenance on its own schedule; a repository whose
              last run is far older than its frequency is the shape of a
              controller that has stopped. The two are shown together so that
              comparison does not need a second screen. */}
          <Property label="Last Maintenance" value={<TimeValue timestamp={data.status?.lastMaintenanceTime} />} />
          <Property label="Maintenance Frequency" value={getBackupRepositoryMaintenanceFrequency(data)} />
        </PropertyList>
      </Section>

      <Section title="Repository" icon={Database} defaultExpanded>
        <PropertyList>
          <Property label="Type" value={getBackupRepositoryType(data)} />
          {/* What this repository is for. Velero partitions repositories by
              namespace, so this is the namespace whose volumes it holds, not
              the namespace the object lives in. */}
          <Property label="Volume Namespace" value={
            volumeNamespace === '-' ? '-' : (
              <ResourceLink
                name={volumeNamespace}
                kind="Namespace"
                namespace=""
                onNavigate={onNavigate}
              />
            )
          } />
          {/* Reachable, like every other location reference in this integration.
              A repository is written into a storage location, and its own status
              says nothing about that location's health. */}
          <Property label="Storage Location" value={
            location === '-' ? '-' : (
              <ResourceLink
                name={location}
                kind="BackupStorageLocation"
                namespace={data?.metadata?.namespace ?? ''}
                group="velero.io"
                onNavigate={onNavigate}
              />
            )
          } />
        </PropertyList>
      </Section>

      <ConditionsSection conditions={status.conditions || []} />
    </>
  )
}
