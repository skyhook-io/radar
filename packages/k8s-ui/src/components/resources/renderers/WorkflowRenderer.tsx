import { Play, Clock, CheckCircle, XCircle, Loader2 } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../../ui/drawer-components'
import { formatAge, formatDuration } from '../resource-utils'

interface WorkflowRendererProps {
  data: any
}

interface WorkflowStep {
  id: string
  displayName: string
  phase: string
  startedAt: string | null
  finishedAt: string | null
}

function getStepDuration(step: WorkflowStep): string | null {
  if (!step.startedAt) return null
  const start = new Date(step.startedAt)
  const end = step.finishedAt ? new Date(step.finishedAt) : new Date()
  return formatDuration(end.getTime() - start.getTime(), true)
}

function StepStatusIcon({ phase }: { phase: string }) {
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
      return 'status-degraded'
    case 'Failed':
    case 'Error':
      return 'status-unhealthy'
    case 'Pending':
      return 'status-degraded'
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

function getWorkflowProblems(data: any): string[] {
  const problems: string[] = []
  const status = data.status || {}
  const phase = status.phase

  if (phase === 'Failed') {
    problems.push(status.message || 'Workflow failed')
  } else if (phase === 'Error') {
    problems.push(status.message || 'Workflow error')
  }

  // Check for failed nodes
  const nodes = status.nodes || {}
  const failedSteps = Object.values(nodes)
    .filter((node: any) => node.type === 'Pod' && node.phase === 'Failed')
    .map((node: any) => node.displayName)

  if (failedSteps.length > 0) {
    problems.push(`Failed steps: ${failedSteps.join(', ')}`)
  }

  return problems
}

function extractSteps(data: any): WorkflowStep[] {
  const nodes = data.status?.nodes || {}
  const steps: WorkflowStep[] = Object.entries(nodes)
    .filter(([, node]: [string, any]) => node.type === 'Pod')
    .map(([id, node]: [string, any]) => ({
      id,
      displayName: node.displayName || id,
      phase: node.phase || 'Pending',
      startedAt: node.startedAt || null,
      finishedAt: node.finishedAt || null,
    }))

  steps.sort((a, b) => {
    if (!a.startedAt && !b.startedAt) return 0
    if (!a.startedAt) return 1
    if (!b.startedAt) return -1
    return new Date(a.startedAt).getTime() - new Date(b.startedAt).getTime()
  })

  return steps
}

export function WorkflowRenderer({ data }: WorkflowRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const phase = status.phase || 'Unknown'

  // Compute duration
  const startedAt = status.startedAt ? new Date(status.startedAt) : null
  const finishedAt = status.finishedAt ? new Date(status.finishedAt) : null
  const duration = startedAt && finishedAt
    ? formatDuration(finishedAt.getTime() - startedAt.getTime(), true)
    : startedAt
    ? formatDuration(Date.now() - startedAt.getTime(), true) + ' (running)'
    : null

  // Extract problems
  const problems = getWorkflowProblems(data)
  const hasProblems = problems.length > 0

  // Extract steps
  const steps = extractSteps(data)

  // Arguments
  const parameters = spec.arguments?.parameters || []

  // Template reference
  const templateName = spec.workflowTemplateRef?.name || null

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
          {templateName && <Property label="Template" value={templateName} />}
          {estimatedDuration != null && (
            <Property label="Estimated Duration" value={formatEstimatedDuration(estimatedDuration)} />
          )}
        </PropertyList>
      </Section>

      {/* Steps section */}
      {steps.length > 0 && (
        <Section title={`Steps (${steps.length})`} defaultExpanded>
          <div className="space-y-1.5">
            {steps.map(step => (
              <div key={step.id} className="flex items-center gap-2 text-sm card-inner px-3 py-2">
                <StepStatusIcon phase={step.phase} />
                <span className="flex-1 text-theme-text-primary">{step.displayName}</span>
                <span className="text-xs text-theme-text-secondary">{getStepDuration(step) || '-'}</span>
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
