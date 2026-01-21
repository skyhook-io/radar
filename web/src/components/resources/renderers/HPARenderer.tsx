import { Cpu } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection } from '../drawer-components'
import { formatAge } from '../resource-utils'

interface HPARendererProps {
  data: any
}

export function HPARenderer({ data }: HPARendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const metrics = status.currentMetrics || []

  return (
    <>
      <Section title="Scaling" icon={Cpu}>
        <PropertyList>
          <Property label="Target" value={`${spec.scaleTargetRef?.kind}/${spec.scaleTargetRef?.name}`} />
          <Property label="Current" value={status.currentReplicas} />
          <Property label="Desired" value={status.desiredReplicas} />
          <Property label="Min" value={spec.minReplicas || 1} />
          <Property label="Max" value={spec.maxReplicas} />
          {status.lastScaleTime && <Property label="Last Scale" value={formatAge(status.lastScaleTime)} />}
        </PropertyList>
      </Section>

      {metrics.length > 0 && (
        <Section title="Metrics" defaultExpanded>
          <div className="space-y-3">
            {metrics.map((metric: any, i: number) => {
              const current = metric.resource?.current?.averageUtilization || metric.resource?.current?.averageValue
              const target = spec.metrics?.[i]?.resource?.target?.averageUtilization || spec.metrics?.[i]?.resource?.target?.averageValue
              return (
                <div key={i} className="bg-theme-elevated/30 rounded p-2">
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

      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
