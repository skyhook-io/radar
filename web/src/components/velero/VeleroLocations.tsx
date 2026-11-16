import { useMemo, useState } from 'react'
import {
  Badge,
  Tooltip,
  VELERO_RUNS_UNKNOWN,
  formatRelativeAgeTime,
  getBSLStatus,
  veleroDefaultText,
  veleroLocationReasonText,
  type VeleroLocationReading,
  veleroLocalTime,
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
    <Tooltip content={veleroLocalTime(l.lastValidation)}>
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
      {/* The usual frequency is in the overdue badge's tooltip; only an unusual one is worth a line. */}
      {(!f || f.disabled) && <Sub>{!f ? 'frequency unknown' : 'validation disabled'}</Sub>}
    </>
  )
}

/** Whether the location is the installation's default, said only when it is or when Radar cannot tell. */
function DefaultMark({ l }: { l: VeleroLocationReading }) {
  const d = veleroDefaultText(l)
  if (d.text === 'No') return null
  return (
    <Tooltip content={d.detail}>
      {d.text === 'Yes' ? (
        <Badge tone="note" size="sm">default</Badge>
      ) : (
        <span className="text-xs text-theme-text-tertiary">default unknown</span>
      )}
    </Tooltip>
  )
}

// A location's row grows with every schedule listed, so long lists fold.
const SCHEDULES_SHOWN = 3

function BackupCount({ l, count }: { l: VeleroLocationReading; count: (b: NonNullable<VeleroLocationReading['backups']>) => number }) {
  if (!l.backups) return <span className="text-theme-text-tertiary">{VELERO_RUNS_UNKNOWN}</span>
  return <span className="font-mono">{count(l.backups)}</span>
}

function SchedulesCell({ l, onInspect }: { l: VeleroLocationReading; onInspect: VeleroScreenProps['onInspect'] }) {
  const [showAll, setShowAll] = useState(false)
  if (l.schedules === null) return <span className="text-theme-text-tertiary">{SCHEDULES_UNKNOWN}</span>
  if (l.schedules.length === 0) return <span className="text-theme-text-tertiary">None</span>
  const shown = showAll ? l.schedules : l.schedules.slice(0, SCHEDULES_SHOWN)
  const hidden = l.schedules.length - shown.length
  return (
    <ul className="space-y-0.5">
      {shown.map((s) => (
        <li key={s.name}>
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation()
              onInspect(veleroResource('Schedule', l.namespace, s.name))
            }}
            className="break-all text-left text-accent-text hover:underline"
          >
            {s.name}
          </button>
          {s.viaDefault && <span className="whitespace-nowrap text-xs text-theme-text-tertiary"> (via default)</span>}
        </li>
      ))}
      {l.schedules.length > SCHEDULES_SHOWN && (
        <li>
          <button
            type="button"
            aria-expanded={showAll}
            onClick={(e) => {
              e.stopPropagation()
              setShowAll((v) => !v)
            }}
            className="text-xs font-medium text-accent-text hover:underline"
          >
            {showAll ? 'Show fewer' : `and ${hidden} more`}
          </button>
        </li>
      )}
    </ul>
  )
}

export function VeleroLocations({ data, installation, onInstallation, onInspect, inspected }: VeleroScreenProps) {
  const multi = data.installations.length > 1
  const locations = useMemo(() => inInstallation(data.locations, installation), [data.locations, installation])

  const columns: TableColumn<VeleroLocationReading>[] = [
    {
      header: 'Location',
      width: '18%',
      cell: (l) => (
        <>
          <span className="inline-flex flex-wrap items-center gap-1.5">
            <span className="break-all font-medium">{l.name}</span>
            <DefaultMark l={l} />
          </span>
          {multi && <Sub>{l.namespace}</Sub>}
          {(l.provider || l.bucket) && <Sub>{[l.provider, l.bucket].filter(Boolean).join(' · ')}</Sub>}
          {l.accessMode === 'ReadOnly' && (
            <Sub>
              <Tooltip content="Velero restores from this location but writes no new backups to it.">
                <span>read-only</span>
              </Tooltip>
            </Sub>
          )}
        </>
      ),
    },
    { header: 'Availability', width: '11%', cell: (l) => <AvailabilityCell l={l} /> },
    { header: 'Last validation', width: '15%', cell: (l) => <ValidationCell l={l} /> },
    { header: 'Backups', width: '10%', cell: (l) => <BackupCount l={l} count={(b) => b.targeting} /> },
    {
      header: (
        <Tooltip content="Completed and not yet expired.">
          <span>Completed</span>
        </Tooltip>
      ),
      width: '10%',
      cell: (l) => <BackupCount l={l} count={(b) => b.completedNotExpired} />,
    },
    { header: 'Expired', width: '9%', cell: (l) => <BackupCount l={l} count={(b) => b.expired} /> },
    { header: 'Schedules writing here', width: '27%', cell: (l) => <SchedulesCell l={l} onInspect={onInspect} /> },
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Storage locations" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <VeleroCoverageNotice data={data} kinds={['backupStorageLocations', 'backups', 'schedules']} />
        <SectionTable
          subtitle={`${locations.length} ${locations.length === 1 ? 'location' : 'locations'}`}
          columns={columns}
          rows={locations}
          rowKey={(l) => `${l.namespace}/${l.name}`}
          rowResource={(l) => veleroResource('BackupStorageLocation', l.namespace, l.name)}
          onInspect={onInspect}
          inspected={inspected}
          empty={coverageEmpty(data.coverage.backupStorageLocations, 'Velero storage locations')}
          minWidth={960}
        />
      </ScreenBody>
    </div>
  )
}
