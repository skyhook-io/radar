import { Activity } from 'lucide-react'
import { Badge } from '../ui/Badge'
import { Tooltip } from '../ui/Tooltip'
import { RefLink, type NavigateToRef } from '../ui/RefLink'
import { Property, PropertyList, Section } from '../ui/drawer-components'
import { healthToSeverity } from '../../utils/badge-colors'
import { formatRelativeAgeTime } from '../../utils/format'
import { getBackupStatus } from '../resources/resource-utils-velero'
import type { VeleroRun, VeleroScheduleReading, VeleroZone } from './types'
import {
  VELERO_COMPLETED_NOTE,
  VELERO_NONE_KEPT,
  VELERO_RUNS_UNKNOWN,
  veleroNewestSuccessfulText,
  veleroNextRunText,
  veleroLocalTime,
  veleroNextRunTitle,
  veleroScheduleReasonText,
  veleroScheduleStateLabel,
  veleroScheduleTone,
  veleroZoneDetail,
  veleroZoneSourceLabel,
} from './reading'

const VELERO_GROUP = 'velero.io'

/** A Velero run's phase as the shared Backup badge; the raw phase shows on hover. */
export function VeleroRunPhase({ phase }: { phase?: string }) {
  const status = getBackupStatus({ status: { phase: phase ?? '' } })
  return (
    <Badge colorClass={status.color} size="sm" title={phase || 'No phase reported yet'}>
      {status.text}
    </Badge>
  )
}

/** A schedule's state as a badge in the shared health vocabulary. */
export function VeleroScheduleStateBadge({ reading }: { reading: Pick<VeleroScheduleReading, 'state' | 'attention'> }) {
  return (
    <Badge severity={healthToSeverity(veleroScheduleTone(reading))} size="sm">
      {veleroScheduleStateLabel(reading.state)}
    </Badge>
  )
}

/** The zone a schedule's times are read in, with how Radar knows it. */
export function VeleroZoneValue({ zone }: { zone: VeleroZone }) {
  return (
    <Tooltip content={veleroZoneDetail(zone)}>
      <span className="text-theme-text-secondary">
        {zone.name} · {veleroZoneSourceLabel(zone.source)}
      </span>
    </Tooltip>
  )
}

export function veleroBackupRef(namespace: string, name: string) {
  return { kind: 'Backup', group: VELERO_GROUP, namespace, name }
}

function RunLine({ run, namespace, onNavigate }: { run: VeleroRun; namespace: string; onNavigate?: NavigateToRef }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
      <RefLink refTo={veleroBackupRef(namespace, run.name)} onNavigate={onNavigate} mono />
      <VeleroRunPhase phase={run.phase} />
      <Tooltip content={veleroLocalTime(run.created)}>
        <span className="text-xs text-theme-text-tertiary">{formatRelativeAgeTime(run.created)}</span>
      </Tooltip>
    </span>
  )
}

/**
 * The drawer section "Schedule activity": the server's reading of a Velero
 * Schedule against its controller's rules, and the schedule's recent runs.
 * Pure: the host fetches the reading and passes it in.
 */
export function VeleroScheduleActivity({
  reading,
  created,
  onNavigate,
}: {
  reading: VeleroScheduleReading
  /** The Schedule's creationTimestamp, for the not-validated sentence. */
  created?: string
  onNavigate?: NavigateToRef
}) {
  const ns = reading.namespace
  const newest = veleroNewestSuccessfulText(reading.runs)
  const recent = reading.runs.recent ?? []
  const nextTitle = veleroNextRunTitle(reading)
  return (
    <Section title="Schedule activity" icon={Activity} defaultExpanded>
      <PropertyList>
        <Property
          label="State"
          value={
            <div className="space-y-1">
              <VeleroScheduleStateBadge reading={reading} />
              <div className="text-sm text-theme-text-secondary">{veleroScheduleReasonText(reading, { created })}</div>
            </div>
          }
        />
        <Property
          label="Next run"
          value={
            reading.nextRun ? (
              <div>
                <Tooltip content={nextTitle}>
                  <span>{veleroNextRunText(reading)}</span>
                </Tooltip>
                <div className="text-xs text-theme-text-tertiary">
                  {veleroLocalTime(reading.nextRun)} · <VeleroZoneValue zone={reading.zone} />
                </div>
              </div>
            ) : (
              <span className="text-theme-text-tertiary">None</span>
            )
          }
        />
        {reading.blocker && (
          <Property
            label={reading.state === 'held' ? 'Held by' : 'Run in progress'}
            value={<RunLine run={reading.blocker} namespace={ns} onNavigate={onNavigate} />}
          />
        )}
        <Property
          label="Newest successful"
          value={
            newest.run ? (
              <RunLine run={newest.run} namespace={ns} onNavigate={onNavigate} />
            ) : (
              <span className="text-theme-text-secondary">{newest.text}</span>
            )
          }
        />
        <Property
          label="Recent runs"
          value={
            reading.runs.read === 'unknown' ? (
              <span className="text-theme-text-secondary">{VELERO_RUNS_UNKNOWN}</span>
            ) : recent.length === 0 ? (
              <span className="text-theme-text-secondary">{VELERO_NONE_KEPT}</span>
            ) : (
              <ul className="space-y-1">
                {recent.map((run) => (
                  <li key={run.name}>
                    <RunLine run={run} namespace={ns} onNavigate={onNavigate} />
                  </li>
                ))}
              </ul>
            )
          }
        />
      </PropertyList>
      <p className="mt-2 text-xs text-theme-text-tertiary">
        Runs are this namespace’s backups labelled with this schedule’s name, among those Velero still keeps; a run’s settings may predate edits to the schedule. {VELERO_COMPLETED_NOTE}
      </p>
    </Section>
  )
}
