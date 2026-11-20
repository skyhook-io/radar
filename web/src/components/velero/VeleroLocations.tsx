import { useMemo, useState } from 'react'
import {
  Badge,
  Tooltip,
  VELERO_RUNS_UNKNOWN,
  formatRelativeAgeTime,
  getBSLStatus,
  veleroDefaultText,
  toneTextClass,
  veleroLocationReasonText,
  veleroPlainReason,
  type VeleroLocationReading,
  veleroLocalTime,
} from '@skyhook-io/k8s-ui'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { ScreenBody, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { coverageEmpty } from '../workspace/coverage'
import { VELERO_WIDE, VeleroCoverageNotice, VeleroName, VeleroWorkspaceHeader, WholeNames, inInstallation, veleroResource, type VeleroScreenProps } from './shared'

const SCHEDULES_UNKNOWN = 'Unknown — no access to schedules'

/** Availability, and under it why a location is unavailable in plain words; Velero's text on hover. */
function AvailabilityCell({ l }: { l: VeleroLocationReading }) {
  const status = getBSLStatus({ status: { phase: l.phase ?? '' } })
  const plain = l.phase === 'Unavailable' ? veleroPlainReason(l.message) : undefined
  return (
    <Tooltip content={l.message} disabled={!l.message} wrapperClassName="flex-col items-start">
      <Badge colorClass={status.color} size="sm">{status.text}</Badge>
      {plain && (
        <div className={`mt-0.5 text-xs ${toneTextClass('unhealthy')}`}>
          <WholeNames text={plain} />
        </div>
      )}
    </Tooltip>
  )
}

// Velero stamps the time of each attempt, whether or not the location passed.
function ValidationCell({ l }: { l: VeleroLocationReading }) {
  const when = l.lastValidation ? (
    <Tooltip content={veleroLocalTime(l.lastValidation)}>
      {l.phase === 'Unavailable' ? (
        <span>
          Last checked {formatRelativeAgeTime(l.lastValidation)} — <span className={toneTextClass('unhealthy')}>failed</span>
        </span>
      ) : (
        <span>{formatRelativeAgeTime(l.lastValidation)}</span>
      )}
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
    <Sub>
      <Tooltip content={d.detail}>
        <span>{d.text === 'Yes' ? 'the default location' : 'default unknown'}</span>
      </Tooltip>
    </Sub>
  )
}

// A location's row grows with every schedule listed, so long lists fold.
const SCHEDULES_SHOWN = 3

function BackupCount({ l, count }: { l: VeleroLocationReading; count: (b: NonNullable<VeleroLocationReading['backups']>) => number }) {
  if (!l.backups) return <span className="text-theme-text-tertiary">{VELERO_RUNS_UNKNOWN}</span>
  return <span className="tabular-nums">{count(l.backups)}</span>
}

/** Completed backups can't be restored while their location is unavailable. */
function NotRestorable({ l }: { l: VeleroLocationReading }) {
  if (l.phase !== 'Unavailable' || !l.backups?.completedNotExpired) return null
  return (
    <div className="mt-0.5">
      <Tooltip content="Velero refuses a restore from an unavailable location, so none of these can be restored until it is available again.">
        <span className={`whitespace-nowrap text-xs ${toneTextClass('unhealthy')}`}>not restorable</span>
      </Tooltip>
    </div>
  )
}

/** The three counts in one cell, for a narrow table: "38 backups", then "27 completed · 9 expired". */
function BackupCountsCell({ l }: { l: VeleroLocationReading }) {
  const b = l.backups
  if (!b) return <span className="text-theme-text-tertiary">{VELERO_RUNS_UNKNOWN}</span>
  return (
    <>
      <span className="tabular-nums">
        {b.targeting} {b.targeting === 1 ? 'backup' : 'backups'}
      </span>
      <Sub>
        <span className="tabular-nums">{b.completedNotExpired}</span> completed · <span className="tabular-nums">{b.expired}</span> expired
      </Sub>
      <NotRestorable l={l} />
    </>
  )
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
            className="block max-w-full text-left text-accent-text hover:underline"
          >
            <VeleroName name={s.name} />
          </button>
          {s.viaDefault && (
            <Tooltip content="The schedule names no location, so its backups go to the installation’s default.">
              <span className="whitespace-nowrap text-xs text-theme-text-tertiary"> (as the default)</span>
            </Tooltip>
          )}
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
  const wide = useMediaQuery(VELERO_WIDE)

  const columns: TableColumn<VeleroLocationReading>[] = [
    {
      header: 'Location',
      width: wide ? '18%' : '22%',
      cell: (l) => (
        <>
          <VeleroName name={l.name} />
          <DefaultMark l={l} />
          {multi && <Sub><WholeNames text={l.namespace} /></Sub>}
          {(l.provider || l.bucket) && (
            <Sub>
              <WholeNames text={[l.provider, l.bucket].filter(Boolean).join(' · ')} />
            </Sub>
          )}
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
    { header: 'Availability', width: wide ? '14%' : '17%', cell: (l) => <AvailabilityCell l={l} /> },
    { header: 'Last validation', width: wide ? '15%' : '18%', cell: (l) => <ValidationCell l={l} /> },
    // Narrow: the three counts share one column.
    ...(wide
      ? [
          { header: 'Backups', width: '10%', cell: (l: VeleroLocationReading) => <BackupCount l={l} count={(b) => b.targeting} /> },
          {
            header: (
              <Tooltip content="Completed and not yet expired. Velero restores only from an available location.">
                <span>Completed</span>
              </Tooltip>
            ),
            width: '10%',
            cell: (l: VeleroLocationReading) => (
              <>
                <BackupCount l={l} count={(b) => b.completedNotExpired} />
                <NotRestorable l={l} />
              </>
            ),
          },
          { header: 'Expired', width: '9%', cell: (l: VeleroLocationReading) => <BackupCount l={l} count={(b) => b.expired} /> },
        ]
      : [{ header: 'Backups', width: '20%', cell: (l: VeleroLocationReading) => <BackupCountsCell l={l} /> }]),
    { header: wide ? 'Schedules writing here' : 'Schedules', width: wide ? '24%' : '23%', cell: (l) => <SchedulesCell l={l} onInspect={onInspect} /> },
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
          minWidth={wide ? 960 : 600}
        />
      </ScreenBody>
    </div>
  )
}
