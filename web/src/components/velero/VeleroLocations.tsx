import { useMemo } from 'react'
import {
  Badge,
  Tooltip,
  VELERO_RUNS_UNKNOWN,
  formatRelativeAgeTime,
  getBSLStatus,
  veleroDefaultText,
  veleroFrequencyText,
  veleroLocationReasonText,
  type VeleroLocationReading,
} from '@skyhook-io/k8s-ui'
import { ScreenBody, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { coverageEmpty } from '../workspace/coverage'
import { VeleroCoverageNotice, VeleroWorkspaceHeader, inInstallation, veleroResource, type VeleroScreenProps } from './shared'

const SCHEDULES_UNKNOWN = 'Unknown — no access to schedules'

function AvailabilityCell({ l }: { l: VeleroLocationReading }) {
  const status = getBSLStatus({ status: { phase: l.phase ?? '' } })
  return (
    <Tooltip content={l.message} disabled={!l.message}>
      <Badge colorClass={status.color} size="sm">{status.text}</Badge>
    </Tooltip>
  )
}

function ValidationCell({ l }: { l: VeleroLocationReading }) {
  const when = l.lastValidation ? (
    <Tooltip content={new Date(l.lastValidation).toLocaleString()}>
      <span>{formatRelativeAgeTime(l.lastValidation)}</span>
    </Tooltip>
  ) : (
    <span className="text-theme-text-tertiary">Not validated yet</span>
  )
  const f = l.frequency
  return (
    <>
      <span className="inline-flex flex-wrap items-center gap-1.5">
        {when}
        {l.validationOverdue && (
          <Tooltip content={veleroLocationReasonText(l)}>
            <Badge severity="warning" size="sm">overdue</Badge>
          </Tooltip>
        )}
      </span>
      <Sub>
        {!f
          ? 'frequency unknown'
          : f.disabled
            ? 'validation disabled'
            : `every ${veleroFrequencyText(f.seconds)} · ${f.source === 'spec' ? 'set on the location' : 'server flag'}`}
      </Sub>
    </>
  )
}

function DefaultCell({ l }: { l: VeleroLocationReading }) {
  const d = veleroDefaultText(l)
  return (
    <Tooltip content={d.detail}>
      <span className={d.text === 'Unknown' ? 'text-theme-text-tertiary' : undefined}>{d.text}</span>
    </Tooltip>
  )
}

function BackupCount({ l, count }: { l: VeleroLocationReading; count: (b: NonNullable<VeleroLocationReading['backups']>) => number }) {
  if (!l.backups) return <span className="text-theme-text-tertiary">{VELERO_RUNS_UNKNOWN}</span>
  return <span className="font-mono">{count(l.backups)}</span>
}

function SchedulesCell({ l }: { l: VeleroLocationReading }) {
  if (l.schedules === null) return <span className="text-theme-text-tertiary">{SCHEDULES_UNKNOWN}</span>
  if (l.schedules.length === 0) return <span className="text-theme-text-tertiary">None</span>
  return (
    <ul className="space-y-0.5">
      {l.schedules.map((s) => (
        <li key={s.name} className="break-all">
          {s.name}
          {s.viaDefault && <span className="text-xs text-theme-text-tertiary"> (via default)</span>}
        </li>
      ))}
    </ul>
  )
}

export function VeleroLocations({ data, installation, onInstallation, onInspect, inspected }: VeleroScreenProps) {
  const multi = data.installations.length > 1
  const locations = useMemo(() => inInstallation(data.locations, installation), [data.locations, installation])
  const unassigned = inInstallation(data.installations, installation).filter((i) => (i.unassignedBackups ?? 0) > 0)

  const columns: TableColumn<VeleroLocationReading>[] = [
    {
      header: 'Location',
      width: '13%',
      cell: (l) => (
        <>
          <span className="break-all font-medium">{l.name}</span>
          {multi && <Sub>{l.namespace}</Sub>}
          {(l.provider || l.bucket) && <Sub>{[l.provider, l.bucket].filter(Boolean).join(' · ')}</Sub>}
        </>
      ),
    },
    { header: 'Availability', width: '10%', cell: (l) => <AvailabilityCell l={l} /> },
    { header: 'Last validation', width: '13%', cell: (l) => <ValidationCell l={l} /> },
    { header: 'Default', width: '7%', cell: (l) => <DefaultCell l={l} /> },
    { header: 'Access mode', width: '9%', cell: (l) => l.accessMode || <span className="text-theme-text-tertiary">ReadWrite</span> },
    { header: 'Backups targeting it', width: '11%', cell: (l) => <BackupCount l={l} count={(b) => b.targeting} /> },
    { header: 'Completed, not expired', width: '11%', cell: (l) => <BackupCount l={l} count={(b) => b.completedNotExpired} /> },
    { header: 'Expired', width: '9%', cell: (l) => <BackupCount l={l} count={(b) => b.expired} /> },
    { header: 'Schedules writing here', width: '17%', cell: (l) => <SchedulesCell l={l} /> },
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Storage locations" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <VeleroCoverageNotice data={data} kinds={['backupStorageLocations', 'backups', 'schedules']} />
        <SectionTable
          title="Storage locations"
          subtitle={`${locations.length} ${locations.length === 1 ? 'location' : 'locations'}`}
          columns={columns}
          rows={locations}
          rowKey={(l) => `${l.namespace}/${l.name}`}
          rowResource={(l) => veleroResource('BackupStorageLocation', l.namespace, l.name)}
          onInspect={onInspect}
          inspected={inspected}
          empty={coverageEmpty(data.coverage.backupStorageLocations, 'Velero storage locations')}
          minWidth={1100}
          footer={
            <>
              Backups are counted by the location each one names; Velero writes it when it first processes the backup.
              {unassigned.map((i) => (
                <div key={i.namespace}>
                  {multi ? `${i.namespace}: ` : ''}
                  {i.unassignedBackups} {i.unassignedBackups === 1 ? 'backup' : 'backups'} not yet assigned a location.
                </div>
              ))}
            </>
          }
        />
      </ScreenBody>
    </div>
  )
}
