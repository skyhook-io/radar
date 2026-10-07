import type { ReactNode } from 'react'
import { Activity, Cpu } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, ResourceLink, type ConditionTone } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import { objectReferenceToResourceRef, apiVersionToGroup } from '../../../utils/navigation'
import type { ResourceRef } from '../../../types'
import { formatAge } from '../resource-utils'
import { HPADiagnosisSummary } from '../HPADiagnosisSummary'
import type { HPADiagnosis } from '../../../types'

interface HPARendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
  hpaDiagnosis?: HPADiagnosis
  /** Optional host-provided section rendered after Conditions — used to inject Prometheus-backed charts. */
  extraSections?: ReactNode
}

function hpaConditionTone(condition: any): ConditionTone | undefined {
  if (condition?.type === 'ScaledToZero') {
    if (condition.status === 'False') return 'ok'
    if (condition.status === 'True') return 'unknown'
    return 'unknown'
  }
  if (condition?.type !== 'ScalingLimited') return undefined
  if (condition.status === 'False') return 'ok'
  if (condition.status !== 'True') return 'unknown'

  const reason = String(condition.reason ?? '').toLowerCase()
  const message = String(condition.message ?? '').toLowerCase()
  if (reason.includes('toomany') || message.includes('maximum')) return 'warning'
  if (reason.includes('toofew') || message.includes('minimum')) return 'ok'
  return 'warning'
}

export function HPARenderer({ data, onNavigate, hpaDiagnosis, extraSections }: HPARendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const metrics = status.currentMetrics || []
  const namespace = data.metadata?.namespace
  const targetRef = spec.scaleTargetRef || {}
  const target = objectReferenceToResourceRef({ ...targetRef, namespace })
  const objectMetrics = (spec.metrics || []).filter((metric: any) => metric.type === 'Object' && metric.object)

  return (
    <>
      {hpaDiagnosis && (
        <Section title="Diagnosis" icon={Activity}>
          <HPADiagnosisSummary diagnosis={hpaDiagnosis} variant="detail" />
        </Section>
      )}

      <Section title="Scaling" icon={Cpu}>
        <PropertyList>
          <Property label="Target" value={
            targetRef.name ? (target ? (
              <ResourceLink {...target} label={`${targetRef.kind}/${targetRef.name}`} onNavigate={onNavigate} />
            ) : `${targetRef.kind || 'Unknown kind'}/${targetRef.name}`) : undefined
          } />
          <Property label="Current" value={status.currentReplicas} />
          <Property label="Desired" value={status.desiredReplicas} />
          <Property label="Min" value={spec.minReplicas ?? 1} />
          <Property label="Max" value={spec.maxReplicas} />
          {status.lastScaleTime && <Property label="Last Scale" value={formatAge(status.lastScaleTime)} />}
        </PropertyList>
      </Section>

      {hpaDiagnosis?.metrics && hpaDiagnosis.metrics.length > 0 && (
        <Section title="Metrics" defaultExpanded>
          <div className="space-y-2">
            {hpaDiagnosis.metrics.map((metric) => (
              <div key={`${metric.type}-${metric.name}`} className="card-inner">
                <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
                  <div>
                    <div className="font-medium text-theme-text-primary">{metric.name}</div>
                    <div className="text-xs text-theme-text-tertiary">{metric.type}</div>
                  </div>
                  <div className="flex flex-wrap items-center gap-2 text-xs text-theme-text-secondary">
                    {metric.current && <span>Current {metric.current}</span>}
                    {metric.target && <span>Target {metric.target}</span>}
                    <Badge severity={metric.status === 'ok' ? 'success' : metric.status === 'missing' ? 'warning' : 'neutral'} size="sm">
                      {metric.status.replace(/_/g, ' ')}
                    </Badge>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </Section>
      )}

      {!hpaDiagnosis?.metrics?.length && metrics.length > 0 && (
        <Section title="Metrics" defaultExpanded>
          <div className="space-y-3">
            {metrics.map((metric: any, i: number) => {
              const current = metric.resource?.current?.averageUtilization || metric.resource?.current?.averageValue
              const target = spec.metrics?.[i]?.resource?.target?.averageUtilization || spec.metrics?.[i]?.resource?.target?.averageValue
              return (
                <div key={i} className="card-inner">
                  <div className="flex items-center justify-between text-sm">
                    <span className="text-theme-text-primary">{metric.resource?.name || metric.type}</span>
                    <span className="text-theme-text-secondary">{current}{typeof current === 'number' ? '%' : ''} / {target}{typeof target === 'number' ? '%' : ''}</span>
                  </div>
                  {typeof current === 'number' && typeof target === 'number' && (
                    <div className="mt-2 h-2 bg-theme-hover rounded overflow-hidden">
                      <div
                        className={clsx(
                          'h-full transition-all',
                          current > target ? 'bg-red-500' : current > target * 0.8 ? 'bg-yellow-500' : 'bg-green-500'
                        )}
                        style={{ width: `${Math.min(100, (current / target) * 100)}%` }}
                      />
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {objectMetrics.length > 0 && (
        <Section title="Object metric sources" defaultExpanded>
          <div className="space-y-3">
            {objectMetrics.map((metric: any, i: number) => {
              const described = metric.object.describedObject || {};
              // The HPA controller measures core Namespace metrics on its own
              // namespace; describedObject.name cannot escape that scope.
              const namespaceMetric =
                described.kind === "Namespace" &&
                described.apiVersion &&
                apiVersionToGroup(described.apiVersion) === "";
              const name = namespaceMetric ? namespace : described.name;
              const source = objectReferenceToResourceRef({
                ...described,
                name,
                namespace,
              });
              return (
                <div
                  key={`${metric.object.metric.name}-${i}`}
                  className="card-inner"
                >
                  <PropertyList>
                    <Property
                      label="Metric"
                      value={metric.object.metric.name}
                    />
                    <Property
                      label="Object"
                      value={
                        source ? (
                          <ResourceLink
                            {...source}
                            label={`${described.kind}/${name}`}
                            onNavigate={onNavigate}
                          />
                        ) : described.name ? (
                          `${described.kind || "Unknown kind"}/${described.name}`
                        ) : undefined
                      }
                    />
                    {namespaceMetric && (
                      <Property label="Scope" value="This HPA's namespace" />
                    )}
                  </PropertyList>
                </div>
              );
            })}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={status.conditions} getConditionTone={hpaConditionTone} />

      {extraSections}
    </>
  )
}
