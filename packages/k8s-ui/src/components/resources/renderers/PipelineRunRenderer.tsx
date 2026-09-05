import { GitBranch, ListChecks } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import { formatAge, formatDuration } from '../resource-utils'
import { buildPipelineTaskGraph, tektonRefName } from '../resource-utils-tekton'

interface PipelineRunRendererProps {
  data: any
}

// The live task-progress DAG lives only in the fullscreen "full view" (see
// PipelineDagView + web/src/components/execution/TektonPipelineFullscreen.tsx,
// wired via renderExpandedOverview) — the compact drawer has no room to
// render a DAG legibly, and clicking a task there needs to open that task's
// own drawer, which the compact view can't do without stacking drawers.
export function PipelineRunRenderer({ data }: PipelineRunRendererProps) {
  const status = data?.status ?? {}
  const conditions = status.conditions ?? []
  const succeededCond = conditions.find((c: any) => c?.type === 'Succeeded')
  const operationalIssuesShown = useOperationalIssuesShown()

  const pipelineSpec = status.pipelineSpec ?? {}
  const taskCount = buildPipelineTaskGraph(pipelineSpec).length

  const isFailed = succeededCond?.status === 'False'
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
          title={succeededCond?.reason || 'PipelineRun failed'}
          message={succeededCond?.message}
        />
      )}

      <Section title="Run Info" icon={ListChecks}>
        <PropertyList>
          <Property label="Pipeline" value={tektonRefName(data?.spec?.pipelineRef)} />
          <Property label="Started" value={startTime ? formatAge(startTime) : undefined} />
          {completionTime && <Property label="Completed" value={formatAge(completionTime)} />}
          {durationMs !== null && <Property label="Duration" value={formatDuration(durationMs, true)} />}
        </PropertyList>
      </Section>

      {taskCount > 0 && (
        <Section title="Task Progress" icon={GitBranch}>
          <p className="text-sm text-theme-text-tertiary">
            {taskCount} task{taskCount === 1 ? '' : 's'} — open the full view (expand icon above) for the live task graph.
          </p>
        </Section>
      )}
    </>
  )
}
