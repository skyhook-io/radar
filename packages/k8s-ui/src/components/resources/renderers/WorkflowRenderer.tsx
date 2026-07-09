import { Play, Clock, CheckCircle, XCircle, Loader2, SkipForward, PauseCircle } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { formatAge, formatDuration } from '../resource-utils'
import { buildWorkflowExecutionModel, WorkflowExecutionNode, WorkflowTemplateReference } from '../../../utils/workflow-execution'

interface WorkflowRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

function getStepDuration(step: WorkflowExecutionNode): string | null {
  if (!step.startedAt) return null
  const start = new Date(step.startedAt)
  const end = step.finishedAt ? new Date(step.finishedAt) : new Date()
  return formatDuration(end.getTime() - start.getTime(), true)
}

function StepStatusIcon({ phase, nodeType }: { phase: string; nodeType?: string }) {
  if (nodeType === 'Skipped' || phase === 'Skipped') {
    return <SkipForward className="w-4 h-4 text-theme-text-tertiary shrink-0" />
  }
  if (nodeType === 'Suspend' && phase !== 'Succeeded') {
    return <PauseCircle className="w-4 h-4 text-yellow-400 shrink-0" />
  }
  switch (phase) {
    case 'Succeeded':
      return <CheckCircle className="w-4 h-4 text-green-400 shrink-0" />
    case 'Failed':
      return <XCircle className="w-4 h-4 text-red-400 shrink-0" />
    case 'Running':
      return <Loader2 className="w-4 h-4 text-yellow-400 shrink-0 animate-spin" />
    default:
      return <Clock className="w-4 h-4 text-theme-text-tertiary shrink-0" />
  }
}

function getPhaseBadgeClass(phase: string): string {
  switch (phase) {
    case 'Succeeded':
      return 'status-healthy'
    case 'Running':
      return 'status-neutral'
    case 'Failed':
    case 'Error':
      return 'status-unhealthy'
    case 'Pending':
      return 'status-degraded'
    case 'Skipped':
    case 'Omitted':
      return 'status-unknown'
    default:
      return 'status-unknown'
  }
}

function formatEstimatedDuration(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  const remainingSeconds = seconds % 60
  if (minutes < 60) {
    return remainingSeconds > 0 ? `${minutes}m ${remainingSeconds}s` : `${minutes}m`
  }
  const hours = Math.floor(minutes / 60)
  const remainingMinutes = minutes % 60
  return remainingMinutes > 0 ? `${hours}h ${remainingMinutes}m` : `${hours}h`
}

function getWorkflowProblems(data: any, steps: WorkflowExecutionNode[]): string[] {
  const problems: string[] = []
  const status = data.status || {}
  const phase = status.phase

  if (phase === 'Failed') {
    problems.push(status.message || 'Workflow failed')
  } else if (phase === 'Error') {
    problems.push(status.message || 'Workflow error')
  }

  const failedNodes = steps.filter((node) => (node.type === 'Pod' || node.type === 'Suspend' || node.type === 'Skipped') && (node.phase === 'Failed' || node.phase === 'Error'))

  if (failedNodes.length > 0) {
    const withMessages = failedNodes.filter((node: any) => node.message)
    if (withMessages.length > 0) {
      for (const n of withMessages as any[]) {
        problems.push(`${n.displayName}: ${n.message}`)
      }
    } else {
      problems.push(`Failed steps: ${failedNodes.map((n: any) => n.displayName).join(', ')}`)
    }
  }

  return problems
}

export function WorkflowRenderer({ data, onNavigate }: WorkflowRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const phase = status.phase || 'Unknown'
  const execution = buildWorkflowExecutionModel(data)
  const steps = execution.visibleSteps

  // Compute duration
  const startedAt = status.startedAt ? new Date(status.startedAt) : null
  const finishedAt = status.finishedAt ? new Date(status.finishedAt) : null
  const duration = startedAt && finishedAt
    ? formatDuration(finishedAt.getTime() - startedAt.getTime(), true)
    : startedAt
    ? formatDuration(Date.now() - startedAt.getTime(), true) + ' (running)'
    : null

  const problems = getWorkflowProblems(data, steps)
  const hasProblems = problems.length > 0

  // Arguments
  const parameters = spec.arguments?.parameters || []

  const workflowTemplateRef = execution.templateRefs.find((ref) => ref.source === 'workflow')

  // Estimated duration
  const estimatedDuration = status.estimatedDuration

  return (
    <>
      {/* Problems alert */}
      {hasProblems && (
        <AlertBanner variant="error" title="Workflow Issues" items={problems} />
      )}

      {/* Success banner */}
      {phase === 'Succeeded' && !hasProblems && (
        <AlertBanner variant="success" title="Workflow Completed Successfully" />
      )}

      {/* Status section */}
      <Section title="Status" icon={Play}>
        <PropertyList>
          <Property label="Phase" value={
            <span className={clsx('badge', getPhaseBadgeClass(phase))}>
              {phase}
            </span>
          } />
          {duration && <Property label="Duration" value={duration} />}
          {status.startedAt && <Property label="Started" value={formatAge(status.startedAt)} />}
          <Property label="Finished" value={status.finishedAt ? formatAge(status.finishedAt) : 'Running...'} />
          {workflowTemplateRef && <Property label="Template" value={<TemplateRefLink refInfo={workflowTemplateRef} onNavigate={onNavigate} />} />}
          {status.progress && (
            <Property label="Progress" value={status.progress} />
          )}
          {estimatedDuration != null && (
            <Property label="Estimated Duration" value={formatEstimatedDuration(estimatedDuration)} />
          )}
          {status.resourcesDuration && (
            <Property
              label="Resource Usage"
              value={
                Object.entries(status.resourcesDuration as Record<string, number>)
                  .map(([resource, seconds]) => `${resource}: ${seconds}s`)
                  .join(', ')
              }
            />
          )}
        </PropertyList>
      </Section>

      {/* Steps section */}
      {steps.length > 0 && (
        <Section title={`Steps (${steps.length})`} defaultExpanded>
          <div className="space-y-1.5">
            {steps.map(step => {
              const isFailed = step.phase === 'Failed' || step.phase === 'Error'
              const isSkipped = step.type === 'Skipped' || step.phase === 'Skipped'
              const isSuspend = step.type === 'Suspend'
              return (
                <div key={step.id} className={clsx(
                  'text-sm card-inner px-3 py-2',
                  isFailed && 'border-l-2 border-red-500'
                )}>
                  <div className="flex items-center gap-2">
                    <StepStatusIcon phase={step.phase} nodeType={step.type} />
                    <span className={clsx(
                      'flex-1',
                      isSkipped ? 'text-theme-text-tertiary' : isSuspend ? 'text-yellow-400' : 'text-theme-text-primary'
                    )}>
                      {step.displayName}
                      {isSkipped && <span className="ml-1 text-xs text-theme-text-tertiary">(skipped)</span>}
                      {isSuspend && <span className="ml-1 text-xs text-yellow-400/70">(suspend)</span>}
                    </span>
                    <span className="text-xs text-theme-text-secondary">{getStepDuration(step) || '-'}</span>
                  </div>
                  {isFailed && step.message && (
                    <div className="text-xs text-red-400 mt-1 ml-6 break-all">{step.message}</div>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {execution.templateRefs.length > 1 && (
        <Section title={`Template References (${execution.templateRefs.length})`} defaultExpanded={false}>
          <div className="space-y-1.5">
            {execution.templateRefs.map((ref) => (
              <div key={`${ref.resourceKind}/${ref.namespace}/${ref.name}/${ref.source}/${ref.template || ''}/${ref.taskName || ''}`} className="flex items-center justify-between gap-3 text-sm card-inner px-3 py-2">
                <div className="min-w-0">
                  <TemplateRefLink refInfo={ref} onNavigate={onNavigate} />
                  {ref.template && <span className="ml-2 text-xs text-theme-text-tertiary">template {ref.template}</span>}
                </div>
                <span className="text-xs text-theme-text-tertiary">{ref.source === 'workflow' ? 'workflow' : ref.taskName || 'task'}</span>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Arguments section */}
      {parameters.length > 0 && (
        <Section title={`Arguments (${parameters.length})`} defaultExpanded={parameters.length <= 5}>
          <PropertyList>
            {parameters.map((param: any) => (
              <Property key={param.name} label={param.name} value={param.value} />
            ))}
          </PropertyList>
        </Section>
      )}

      {/* Conditions section */}
      <ConditionsSection conditions={status.conditions} />
    </>
  )
}

function TemplateRefLink({ refInfo, onNavigate }: { refInfo: WorkflowTemplateReference; onNavigate?: WorkflowRendererProps['onNavigate'] }) {
  return (
    <ResourceLink
      name={refInfo.name}
      kind={refInfo.resourceKind}
      namespace={refInfo.namespace}
      group="argoproj.io"
      label={refInfo.clusterScope ? `${refInfo.name} (cluster)` : refInfo.name}
      onNavigate={onNavigate}
    />
  )
}
