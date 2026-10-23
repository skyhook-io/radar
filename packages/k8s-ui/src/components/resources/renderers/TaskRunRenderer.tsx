import { CheckCircle2, CircleDashed, ListChecks, Loader2, Terminal, XCircle } from 'lucide-react'
import { clsx } from 'clsx'
import { AlertBanner, PropertyList, Property, ResourceLink, Section, useOperationalIssuesShown } from '../../ui/drawer-components'
import { formatAge, formatDuration } from '../resource-utils'
import { tektonRefName } from '../resource-utils-tekton'
import type { ResourceRef } from '../../../types/core'

interface TaskRunRendererProps {
  data: any
  // Absent when the TaskRun's pod is already gone (GC'd after completion —
  // common by the time someone opens an old TaskRun) or the host doesn't
  // wire log viewing for this kind.
  onViewLogs?: (podName: string, containerName: string) => void
  onNavigate?: (ref: ResourceRef) => void
}

// Tekton reports the real container name at status.steps[].container — read
// that first. The `step-<stepName>` convention is only a fallback for a
// status shape that's missing the field (an older/stripped object); Tekton
// doesn't guarantee the naive prefix once a step name gets sanitized or
// truncated to fit Kubernetes' container-name limits.
function stepContainerName(step: { name: string; container?: string }): string {
  return step.container ?? `step-${step.name}`
}

function StepRow({ step, podName, onViewLogs }: { step: any; podName?: string; onViewLogs?: (podName: string, containerName: string) => void }) {
  const terminated = step.terminated
  const running = 'running' in step
  const waiting = step.waiting
  const Icon = terminated ? (terminated.exitCode === 0 ? CheckCircle2 : XCircle) : running ? Loader2 : CircleDashed
  const tone = terminated
    ? terminated.exitCode === 0
      ? 'text-emerald-500'
      : 'text-red-500'
    : running
      ? 'text-sky-500'
      : 'text-theme-text-tertiary'
  const canViewLogs = Boolean(onViewLogs && podName)
  return (
    <div className="flex items-start gap-2 py-1.5 text-sm">
      <Icon className={clsx('mt-0.5 h-4 w-4 shrink-0', tone, running && 'animate-spin')} />
      <div className="min-w-0 flex-1">
        <div className="font-medium text-theme-text-primary">{step.name}</div>
        {terminated && (
          <div className="text-xs text-theme-text-tertiary">
            {terminated.reason}{terminated.exitCode !== undefined ? ` · exit ${terminated.exitCode}` : ''}
          </div>
        )}
        {waiting?.reason && <div className="text-xs text-theme-text-tertiary">Waiting: {waiting.reason}</div>}
      </div>
      {canViewLogs && (
        <button
          type="button"
          onClick={() => onViewLogs!(podName!, stepContainerName(step))}
          className="inline-flex shrink-0 items-center gap-1 rounded px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary"
          title={`View logs for step "${step.name}"`}
        >
          <Terminal className="h-3.5 w-3.5" />
          Logs
        </button>
      )}
    </div>
  )
}

export function TaskRunRenderer({ data, onViewLogs, onNavigate }: TaskRunRendererProps) {
  const spec = data?.spec ?? {}
  const status = data?.status ?? {}
  const conditions = status.conditions ?? []
  const succeededCond = conditions.find((c: any) => c?.type === 'Succeeded')
  const isFailed = succeededCond?.status === 'False'
  const operationalIssuesShown = useOperationalIssuesShown()

  const steps = status.steps ?? []
  const results = status.results ?? []
  const params = spec.params ?? []

  const startTime = status.startTime
  const completionTime = status.completionTime
  const durationMs = startTime
    ? (completionTime ? new Date(completionTime).getTime() : Date.now()) - new Date(startTime).getTime()
    : null

  return (
    <>
      {isFailed && !operationalIssuesShown && (
        <AlertBanner
          variant="error"
          title={succeededCond?.reason || 'TaskRun failed'}
          message={succeededCond?.message}
        />
      )}

      {steps.length > 0 && (
        <Section title="Steps" icon={Terminal}>
          <div className="divide-y divide-theme-border">
            {steps.map((step: any) => (
              <StepRow key={step.name} step={step} podName={status.podName} onViewLogs={onViewLogs} />
            ))}
          </div>
        </Section>
      )}

      <Section title="Run Info" icon={ListChecks}>
        <PropertyList>
          <Property label="Task" value={tektonRefName(spec.taskRef)} />
          <Property
            label="Pod"
            value={status.podName && (
              <ResourceLink kind="pods" namespace={data?.metadata?.namespace ?? ''} name={status.podName} onNavigate={onNavigate} />
            )}
          />
          <Property label="Started" value={startTime ? formatAge(startTime) : undefined} />
          {completionTime && <Property label="Completed" value={formatAge(completionTime)} />}
          {durationMs !== null && <Property label="Duration" value={formatDuration(durationMs, true)} />}
        </PropertyList>
      </Section>

      {params.length > 0 && (
        <Section title="Parameters">
          <PropertyList>
            {params.map((p: any) => (
              <Property key={p.name} label={p.name} value={typeof p.value === 'string' ? p.value : JSON.stringify(p.value)} />
            ))}
          </PropertyList>
        </Section>
      )}

      {results.length > 0 && (
        <Section title="Results">
          <PropertyList>
            {results.map((r: any) => (
              <Property key={r.name} label={r.name} value={typeof r.value === 'string' ? r.value : JSON.stringify(r.value)} />
            ))}
          </PropertyList>
        </Section>
      )}
    </>
  )
}
