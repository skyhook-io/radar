import { Activity, Boxes, Server } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner, ConditionsSection } from '../../ui/drawer-components'
import { formatAge } from '../resource-utils'
import { canaryStepTemplateRefs } from './RolloutRenderer'

interface ExperimentRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function phaseClass(phase?: string): string {
  switch (phase) {
    case 'Successful':
      return 'status-healthy'
    case 'Running':
    case 'Progressing':
    case 'Pending':
      return 'status-degraded'
    case 'Failed':
    case 'Error':
      return 'status-unhealthy'
    default:
      return 'status-unknown'
  }
}

// Experiments run one or more Deployment-shaped templates side by side
// (often unweighted "just try it" baselines, not always traffic-split) for
// spec.duration, optionally gated by the same AnalysisTemplate references a
// canary analysis step uses. No live demo fixture exists for this CRD (see
// scripts/rollouts-demo/README.md) — built against the public schema.
export function ExperimentRenderer({ data, onNavigate }: ExperimentRendererProps) {
  const spec = data?.spec || {}
  const status = data?.status || {}
  const phase = status.phase || 'Unknown'
  const templates: any[] = spec.templates || []
  const templateStatuses: any[] = status.templateStatuses || []
  const analyses: any[] = spec.analyses || []
  // The controller's real field is analysisRuns (verified live) — each entry
  // is {name, analysisRun, phase}, not the analysisRunStatuses/status shape
  // the public docs' examples suggest.
  const analysisRunStatuses: any[] = status.analysisRuns || []

  const templateStatusByName = new Map(templateStatuses.map((t) => [t.name, t]))
  const analysisRunStatusByName = new Map(analysisRunStatuses.map((a) => [a.name, a]))

  const ownerRollout = (data?.metadata?.ownerReferences || []).find((ref: any) => ref.kind === 'Rollout')

  return (
    <>
      {(phase === 'Failed' || phase === 'Error') && (
        <AlertBanner
          variant="error"
          title={phase === 'Error' ? 'Experiment could not run' : 'Experiment failed'}
          message={status.message || 'One or more templates or analyses did not succeed.'}
        />
      )}

      <Section title="Status" icon={Server}>
        <PropertyList>
          <Property label="Phase" value={<span className={clsx('badge', phaseClass(phase))}>{phase}</span>} />
          <Property label="Duration" value={spec.duration} />
          <Property label="Message" value={status.message} />
          <Property label="Available At" value={status.availableAt ? formatAge(status.availableAt) : undefined} />
          {ownerRollout && (
            <Property
              label="Rollout"
              value={
                onNavigate ? (
                  <button
                    onClick={() => onNavigate({ kind: 'Rollout', namespace: data?.metadata?.namespace ?? '', name: ownerRollout.name })}
                    className="text-brand hover:underline"
                  >
                    {ownerRollout.name}
                  </button>
                ) : (
                  ownerRollout.name
                )
              }
            />
          )}
        </PropertyList>
      </Section>

      {templates.length > 0 && (
        <Section title={`Templates (${templates.length})`} icon={Boxes} defaultExpanded>
          <div className="space-y-2">
            {templates.map((tmpl: any) => {
              const tmplStatus = templateStatusByName.get(tmpl.name)
              return (
                <div key={tmpl.name} className="card-inner space-y-1.5">
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-mono text-sm text-theme-text-primary">{tmpl.name}</span>
                    {tmplStatus?.status && <span className={clsx('badge-sm', phaseClass(tmplStatus.status))}>{tmplStatus.status}</span>}
                  </div>
                  <PropertyList>
                    <Property label="Replicas" value={tmpl.replicas} />
                    {tmpl.weight !== undefined && <Property label="Weight" value={`${tmpl.weight}%`} />}
                    {tmplStatus && (
                      <Property
                        label="Ready"
                        value={`${tmplStatus.readyReplicas ?? 0}/${tmplStatus.replicas ?? tmpl.replicas ?? 0}`}
                      />
                    )}
                    {tmplStatus?.podTemplateHash && <Property label="Pod Hash" value={tmplStatus.podTemplateHash} />}
                  </PropertyList>
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {analyses.length > 0 && (
        <Section title={`Analyses (${analyses.length})`} icon={Activity} defaultExpanded>
          <PropertyList>
            {analyses.map((analysis: any) => {
              const runStatus = analysisRunStatusByName.get(analysis.name)
              const refs = canaryStepTemplateRefs(analysis)
              return (
                <Property
                  key={analysis.name}
                  label={analysis.name}
                  value={
                    <span className="flex flex-wrap items-center gap-2">
                      {runStatus?.phase && (
                        <span className={clsx('badge-sm', phaseClass(runStatus.phase))}>{runStatus.phase}</span>
                      )}
                      {refs.map((ref) =>
                        onNavigate ? (
                          <button
                            key={ref.name}
                            onClick={() =>
                              onNavigate({
                                kind: ref.clusterScoped ? 'ClusterAnalysisTemplate' : 'AnalysisTemplate',
                                namespace: ref.clusterScoped ? '' : data?.metadata?.namespace ?? '',
                                name: ref.name,
                              })
                            }
                            className="font-mono text-xs text-brand hover:underline"
                          >
                            {ref.name}
                          </button>
                        ) : (
                          <span key={ref.name} className="font-mono text-xs text-theme-text-secondary">{ref.name}</span>
                        )
                      )}
                      {runStatus?.analysisRun && onNavigate && (
                        <button
                          onClick={() => onNavigate({ kind: 'AnalysisRun', namespace: data?.metadata?.namespace ?? '', name: runStatus.analysisRun })}
                          className="font-mono text-xs text-theme-text-tertiary hover:underline"
                        >
                          run: {runStatus.analysisRun}
                        </button>
                      )}
                    </span>
                  }
                />
              )
            })}
          </PropertyList>
        </Section>
      )}

      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
