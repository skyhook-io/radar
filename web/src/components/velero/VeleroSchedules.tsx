import {
  CronValue,
  Tooltip,
  VELERO_NONE_KEPT,
  VELERO_NONE_KEPT_SHORT,
  VELERO_RUNS_UNKNOWN,
  VeleroRunPhase,
  VeleroScheduleStateBadge,
  VeleroZoneValue,
  formatRelativeAgeTime,
  veleroNextRunText,
  veleroNextRunTitle,
  veleroScheduleReasonText,
  veleroLocalTime,
  type VeleroScheduleReading,
} from '@skyhook-io/k8s-ui'
import { useMemo } from 'react'
import { ScreenBody, SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { coverageEmpty } from '../workspace/coverage'
import { VeleroCoverageNotice, VeleroWorkspaceHeader, inInstallation, veleroResource, type VeleroScreenProps } from './shared'

/** The newest run, and the newest success only when the newest run isn't one. */
function NewestRunCell({ s, onInspect }: { s: VeleroScheduleReading; onInspect: VeleroScreenProps['onInspect'] }) {
  if (s.runs.read === 'unknown') return <span className="text-theme-text-tertiary">{VELERO_RUNS_UNKNOWN}</span>
  const run = s.runs.newest
  if (!run) return <NoneKept />
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
        <span className="text-xs text-theme-text-tertiary">{formatRelativeAgeTime(run.ranAt)}</span>
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
  if (!s.location.viaDefault) return <span className="break-all">{s.location.name}</span>
  return (
    <Tooltip content="The schedule names no location, so its backups go to the installation’s default.">
      <span>
        <span className="break-all">{s.location.name || 'Unresolved'}</span>
        <Sub>via default</Sub>
      </span>
    </Tooltip>
  )
}

/**
 * Every schedule read against Velero's controller rules: its state, next run,
 * last submission and newest run. The Velero Schedule kind's list.
 */
export function VeleroSchedules({ data, installation, onInstallation, onInspect, inspected }: VeleroScreenProps) {
  const multi = data.installations.length > 1
  const schedules = useMemo(() => inInstallation(data.schedules, installation), [data.schedules, installation])
  const zone = sharedZone(schedules)

  const columns: TableColumn<VeleroScheduleReading>[] = [
    {
      header: 'Schedule',
      width: '15%',
      cell: (s) => (
        <>
          <span className="break-all font-medium">{s.name}</span>
          {multi && <Sub>{s.namespace}</Sub>}
        </>
      ),
    },
    {
      header: zone ? `Cron (${zone})` : 'Cron',
      width: '16%',
      cell: (s) => (
        <>
          <CronValue cron={s.cron} />
          {!zone && <Sub><VeleroZoneValue zone={s.zone} /></Sub>}
        </>
      ),
    },
    {
      header: 'State',
      width: '12%',
      cell: (s) => (
        <Tooltip content={veleroScheduleReasonText(s)}>
          <VeleroScheduleStateBadge reading={s} />
        </Tooltip>
      ),
    },
    {
      header: 'Next run',
      width: '12%',
      cell: (s) =>
        s.nextRun ? (
          <>
            <Tooltip content={veleroNextRunTitle(s)}>
              <span>{veleroNextRunText(s)}</span>
            </Tooltip>
            <Sub>{veleroLocalTime(s.nextRun)}</Sub>
          </>
        ) : (
          <span className="text-theme-text-tertiary">—</span>
        ),
    },
    {
      header: 'Last submission',
      width: '11%',
      cell: (s) =>
        s.lastSubmitted ? (
          <Tooltip content={veleroLocalTime(s.lastSubmitted)}>
            <span>{formatRelativeAgeTime(s.lastSubmitted)}</span>
          </Tooltip>
        ) : (
          <span className="text-theme-text-tertiary">Never</span>
        ),
    },
    { header: 'Newest run', width: '18%', cell: (s) => <NewestRunCell s={s} onInspect={onInspect} /> },
    { header: 'Location', width: '13%', cell: (s) => <LocationCell s={s} /> },
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
