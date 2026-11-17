import { Activity } from 'lucide-react'
import { Badge } from '../ui/Badge'
import { Tooltip } from '../ui/Tooltip'
import { type NavigateToRef } from '../ui/RefLink'
import { Property, PropertyList, Section } from '../ui/drawer-components'
import { healthToSeverity } from '../../utils/badge-colors'
import { formatRelativeAgeTime } from '../../utils/format'
import { getBackupStatus } from '../resources/resource-utils-velero'
import type { VeleroRun, VeleroScheduleReading, VeleroZone } from './types'
import {
  VELERO_NONE_KEPT,
  VELERO_RUNS_UNKNOWN,
  veleroNewestSuccessfulText,
  veleroNextRunText,
  veleroLocalTime,
  veleroNextRunTitle,
  veleroScheduleReasonText,
  veleroScheduleBadge,
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

/** A schedule's state as a badge in the shared health vocabulary (see veleroScheduleBadge). */
export function VeleroScheduleStateBadge({ reading }: { reading: Parameters<typeof veleroScheduleBadge>[0] }) {
  const badge = veleroScheduleBadge(reading)
  return (
    <Badge severity={healthToSeverity(badge.tone)} size="sm">
      {badge.label}
    </Badge>
  )
}

/** The zone a schedule's times are read in, with how Radar knows it. */
export function VeleroZoneValue({ zone }: { zone: VeleroZone }) {
  return (
    <Tooltip content={veleroZoneDetail(zone)}>
      <span className="text-theme-text-secondary">
        {zone.name} — {veleroZoneSourceLabel(zone.source)}
      </span>
    </Tooltip>
  )
}

export function veleroBackupRef(namespace: string, name: string) {
  return { kind: 'Backup', group: VELERO_GROUP, namespace, name }
}

/**
 * One run on one line: its outcome and age. The backup's name is the
 * schedule's plus a timestamp, so it goes in the tooltip, and the line opens it.
 */
function RunLine({ run, namespace, onNavigate }: { run: VeleroRun; namespace: string; onNavigate?: NavigateToRef }) {
  const content = (
    <span className="inline-flex items-center gap-2">
      <VeleroRunPhase phase={run.phase} />
      <span className="text-xs text-theme-text-tertiary">{formatRelativeAgeTime(run.ranAt)}</span>
    </span>
  )
  return (
    <Tooltip content={`${run.name} · ${veleroLocalTime(run.ranAt)}`}>
      {onNavigate ? (
        <button type="button" onClick={() => onNavigate(veleroBackupRef(namespace, run.name))} aria-label={`Open backup ${run.name}`} className="rounded hover:bg-theme-hover">
          {content}
        </button>
      ) : (
        content
      )}
    </Tooltip>
  )
}

/** Recent runs shown in the drawer; the rest are a click away in the Backups list. */
const RECENT_SHOWN = 5

/**
 * The drawer section "Schedule activity": the server's reading of a Velero
 * Schedule against its controller's rules, and the schedule's recent runs.
 * Pure: the host fetches the reading and passes it in.
 */
export function VeleroScheduleActivity({
  reading,
  created,
  onNavigate,
  onShowAllRuns,
}: {
  reading: VeleroScheduleReading
  /** The Schedule's creationTimestamp, for the not-validated sentence. */
  created?: string
  onNavigate?: NavigateToRef
  /** Opens every run Velero keeps for this schedule. Without it there is no link. */
  onShowAllRuns?: () => void
}) {
  const ns = reading.namespace
  const newest = veleroNewestSuccessfulText(reading.runs)
  const recent = reading.runs.recent ?? []
  const nextTitle = veleroNextRunTitle(reading)
  const runsUnknown = reading.runs.read === 'unknown'
  // The header chip already shows the state; this says why. Runs are said once:
  // nothing kept is one line, and the newest success shows only when it isn't
  // already the newest run listed.
  const noneKept = !runsUnknown && recent.length === 0 && !newest.run
  // The run holding the schedule has its own row.
  const others = recent.filter((run) => run.name !== reading.blocker?.name).slice(0, RECENT_SHOWN)
  const kept = reading.runs.kept ?? 0
  // No success at all is always said; a success already listed first isn't.
  const showNewest = !runsUnknown && !noneKept && (!newest.run || newest.run.name !== others[0]?.name)
  return (
    <Section title="Schedule activity" icon={Activity} defaultExpanded>
      <PropertyList>
        <Property
          label="Status"
          value={<div className="text-sm text-theme-text-secondary">{veleroScheduleReasonText(reading, { created })}</div>}
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
                  {veleroLocalTime(reading.nextRun)}
                  {(reading.zone.source === 'inferred' || reading.zone.source === 'unknown') && ' · Velero’s time zone not confirmed'}
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
        {noneKept ? (
          <Property label="Runs" value={<span className="text-theme-text-secondary">{VELERO_NONE_KEPT}</span>} />
        ) : (
          <>
            {showNewest && (
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
            )}
            {(runsUnknown || others.length > 0) && <Property
              label="Recent runs"
              value={
                runsUnknown ? (
                  <span className="text-theme-text-secondary">{VELERO_RUNS_UNKNOWN}</span>
                ) : (
                  <div className="space-y-1">
                    <ul className="space-y-0.5">
                      {others.map((run) => (
                        <li key={run.name}>
                          <RunLine run={run} namespace={ns} onNavigate={onNavigate} />
                        </li>
                      ))}
                    </ul>
                    {onShowAllRuns && kept > others.length && (
                      <button type="button" onClick={onShowAllRuns} className="text-xs text-accent-text hover:underline">
                        All {kept} runs →
                      </button>
                    )}
                  </div>
                )
              }
            />}
          </>
        )}
      </PropertyList>
    </Section>
  )
}
