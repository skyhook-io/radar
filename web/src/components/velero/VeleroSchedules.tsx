import {
  Tooltip,
  VELERO_NONE_KEPT,
  VELERO_NONE_KEPT_SHORT,
  VELERO_RUNS_UNKNOWN,
  VeleroRunPhase,
  VeleroScheduleStateBadge,
  VeleroZoneValue,
  formatRelativeAgeTime,
  veleroNextRunText,
  veleroCadenceText,
  veleroNextRunTitle,
  veleroScheduleBadge,
  veleroScheduleReasonText,
  veleroLocalTime,
  type VeleroScheduleReading,
} from '@skyhook-io/k8s-ui'
import { useMemo } from 'react'
import { ScreenBody, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { coverageEmpty } from '../workspace/coverage'
import { VeleroCoverageNotice, VeleroName, VeleroWorkspaceHeader, inInstallation, veleroResource, type VeleroScreenProps } from './shared'

/**
 * The schedule's last run: the newest one Velero keeps, with the newest
 * success when that run wasn't one; when the runs are not kept or not read,
 * when Velero last submitted one.
 */
function LastRunCell({ s, onInspect }: { s: VeleroScheduleReading; onInspect: VeleroScreenProps['onInspect'] }) {
  const submitted = s.lastSubmitted ? (
    <Tooltip content={veleroLocalTime(s.lastSubmitted)}>
      <span>submitted {formatRelativeAgeTime(s.lastSubmitted)}</span>
    </Tooltip>
  ) : null
  if (s.runs.read === 'unknown') {
    return (
      <>
        {submitted ?? <span className="text-theme-text-tertiary">{VELERO_RUNS_UNKNOWN}</span>}
        {submitted && <Sub>{VELERO_RUNS_UNKNOWN}</Sub>}
      </>
    )
  }
  const run = s.runs.newest
  if (!run) {
    if (!submitted) return <span className="text-theme-text-tertiary">Never</span>
    return (
      <>
        {submitted}
        <Sub><NoneKept /></Sub>
      </>
    )
  }
  const success = s.runs.newestCompleted
  return (
    <>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          onInspect(veleroResource('Backup', s.namespace, run.name))
        }}
        aria-label={`Open backup ${run.name}`}
        className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-left"
      >
        <VeleroRunPhase phase={run.phase} />
        <Tooltip content={veleroLocalTime(run.ranAt)}>
          <span className="text-xs text-theme-text-tertiary">{formatRelativeAgeTime(run.ranAt)}</span>
        </Tooltip>
      </button>
      {success?.name !== run.name && (
        <Sub>
          {success ? (
            <Tooltip content={veleroLocalTime(success.completed ?? success.ranAt)}>
              <span>last success {formatRelativeAgeTime(success.completed ?? success.ranAt)}</span>
            </Tooltip>
          ) : (
            'no success kept'
          )}
        </Sub>
      )}
    </>
  )
}

function NoneKept() {
  return (
    <Tooltip content={VELERO_NONE_KEPT}>
      <span className="text-theme-text-tertiary">{VELERO_NONE_KEPT_SHORT}</span>
    </Tooltip>
  )
}

/** The zone every schedule's cron is read in, when they share one Radar is sure of. */
function sharedZone(schedules: VeleroScheduleReading[]): string | null {
  const names = new Set(schedules.map((s) => (s.zone.source === 'inferred' || s.zone.source === 'unknown' ? '\u0000' : s.zone.name)))
  return names.size === 1 && !names.has('\u0000') ? [...names][0] : null
}

function LocationCell({ s }: { s: VeleroScheduleReading }) {
  if (!s.location.viaDefault) return <VeleroName name={s.location.name ?? ''} />
  return (
    <>
      {s.location.name ? <VeleroName name={s.location.name} /> : <span className="text-theme-text-tertiary">Unresolved</span>}
      <Tooltip content="The schedule names no location, so its backups go to the installation’s default.">
        <Sub>the default; not set on the schedule</Sub>
      </Tooltip>
    </>
  )
}

/** Due within its grace reads on schedule (see veleroScheduleBadge), so its next run reads "due now", not "was due". */
export function NextRunCell({ s }: { s: VeleroScheduleReading }) {
  if (!s.nextRun) return <span className="text-theme-text-tertiary">—</span>
  const dueNow = s.state === 'due' && s.reason !== 'serverDown'
  return (
    <>
      <Tooltip content={veleroNextRunTitle(s)}>
        <span>{dueNow ? 'due now' : veleroNextRunText(s)}</span>
      </Tooltip>
      <Sub>{veleroLocalTime(s.nextRun)}</Sub>
    </>
  )
}

/**
 * Every schedule read against Velero's controller rules: its state, next run
 * and last run. The Velero Schedule kind's list.
 */
export function VeleroSchedules({ data, installation, onInstallation, onInspect, inspected }: VeleroScreenProps) {
  const multi = data.installations.length > 1
  const schedules = useMemo(() => inInstallation(data.schedules, installation), [data.schedules, installation])
  const zone = sharedZone(schedules)

  const columns: TableColumn<VeleroScheduleReading>[] = [
    {
      header: 'Schedule',
      width: '20%',
      cell: (s) => (
        <>
          <VeleroName name={s.name} />
          {multi && <Sub>{s.namespace}</Sub>}
        </>
      ),
    },
    {
      header: zone ? `Runs (${zone})` : 'Runs',
      width: '16%',
      cell: (s) => {
        const cadence = veleroCadenceText(s.cron)
        return (
          <>
            {cadence ? <span>{cadence}</span> : <span className="font-mono text-sm">{s.cron}</span>}
            {cadence && <div className="mt-0.5 font-mono text-xs text-theme-text-tertiary">{s.cron}</div>}
            {!zone && <Sub><VeleroZoneValue zone={s.zone} /></Sub>}
          </>
        )
      },
    },
    {
      header: 'State',
      width: '13%',
      cell: (s) => (
        <Tooltip content={veleroScheduleBadge(s).detail ?? veleroScheduleReasonText(s)}>
          <VeleroScheduleStateBadge reading={s} />
        </Tooltip>
      ),
    },
    { header: 'Next run', width: '13%', cell: (s) => <NextRunCell s={s} /> },
    { header: 'Last run', width: '20%', cell: (s) => <LastRunCell s={s} onInspect={onInspect} /> },
    { header: 'Location', width: '18%', cell: (s) => <LocationCell s={s} /> },
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Schedules" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <VeleroCoverageNotice data={data} kinds={['schedules', 'backups']} />
        <SectionTable
          subtitle={`${schedules.length} ${schedules.length === 1 ? 'schedule' : 'schedules'}`}
          columns={columns}
          rows={schedules}
          rowKey={(s) => `${s.namespace}/${s.name}`}
          rowResource={(s) => veleroResource('Schedule', s.namespace, s.name)}
          onInspect={onInspect}
          inspected={inspected}
          empty={coverageEmpty(data.coverage.schedules, 'Velero schedules')}
          minWidth={980}
        />
      </ScreenBody>
    </div>
  )
}
