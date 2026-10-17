import { ListChecks, Package } from 'lucide-react'
import { Section, PropertyList, Property } from '../../ui/drawer-components'
import { buildPipelineTaskGraph } from '../resource-utils-tekton'

interface PipelineRendererProps {
  data: any
}

// The task-dependency DAG lives only in the fullscreen "full view" (see
// PipelineDagView + web/src/components/execution/TektonPipelineFullscreen.tsx,
// wired via renderExpandedOverview) — the compact drawer has no room to
// render it legibly, so this overview stays graph-free.
export function PipelineRenderer({ data }: PipelineRendererProps) {
  const spec = data?.spec ?? {}
  const params = spec.params ?? []
  const workspaces = spec.workspaces ?? []
  // buildPipelineTaskGraph (not a bare spec.tasks.length) so finally tasks
  // count toward the total — Tekton runs them and includes their outcome in
  // the PipelineRun result same as any regular task.
  const taskCount = buildPipelineTaskGraph(spec).length

  return (
    <>
      <Section title="Pipeline Info">
        <PropertyList>
          <Property label="Tasks" value={String(taskCount)} />
        </PropertyList>
      </Section>

      {params.length > 0 && (
        <Section title="Parameters" icon={ListChecks}>
          <PropertyList>
            {params.map((p: any) => (
              <Property
                key={p.name}
                label={p.name}
                value={p.default !== undefined ? `${JSON.stringify(p.default)}${p.type ? ` (${p.type})` : ''}` : (p.type ?? 'string')}
              />
            ))}
          </PropertyList>
        </Section>
      )}

      {workspaces.length > 0 && (
        <Section title="Workspaces" icon={Package}>
          <PropertyList>
            {workspaces.map((w: any) => (
              <Property key={w.name} label={w.name} value={w.optional ? 'optional' : 'required'} />
            ))}
          </PropertyList>
        </Section>
      )}

      {spec.description && (
        <Section title="Description">
          <p className="text-sm text-theme-text-secondary whitespace-pre-wrap">{spec.description}</p>
        </Section>
      )}
    </>
  )
}
