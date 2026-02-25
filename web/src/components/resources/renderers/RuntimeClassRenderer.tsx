import { Cpu } from 'lucide-react'
import { Section, PropertyList, Property } from '../drawer-components'

interface RuntimeClassRendererProps {
  data: any
}

export function RuntimeClassRenderer({ data }: RuntimeClassRendererProps) {
  const overhead = data.overhead?.podFixed
  const hasOverhead = overhead && Object.keys(overhead).length > 0
  const scheduling = data.scheduling
  const nodeSelector = scheduling?.nodeSelector
  const hasNodeSelector = nodeSelector && Object.keys(nodeSelector).length > 0
  const tolerations = scheduling?.tolerations
  const hasTolerations = tolerations && tolerations.length > 0

  return (
    <>
      <Section title="Runtime" icon={Cpu}>
        <PropertyList>
          <Property label="Handler" value={data.handler} />
        </PropertyList>
      </Section>

      {hasOverhead && (
        <Section title="Overhead">
          <PropertyList>
            {Object.entries(overhead).map(([resource, quantity]) => (
              <Property key={resource} label={resource} value={quantity as string} />
            ))}
          </PropertyList>
        </Section>
      )}

      {(hasNodeSelector || hasTolerations) && (
        <Section title="Scheduling">
          <div className="space-y-3">
            {hasNodeSelector && (
              <div>
                <div className="text-xs text-theme-text-tertiary mb-1">Node Selector</div>
                <div className="flex flex-wrap gap-1">
                  {Object.entries(nodeSelector).map(([key, value]) => (
                    <span
                      key={key}
                      className="px-2 py-0.5 rounded text-xs bg-blue-500/20 text-blue-400"
                    >
                      {key}={value as string}
                    </span>
                  ))}
                </div>
              </div>
            )}
            {hasTolerations && (
              <div>
                <div className="text-xs text-theme-text-tertiary mb-1">Tolerations</div>
                <div className="space-y-1.5">
                  {tolerations.map((t: any, i: number) => {
                    const parts = [t.key, t.operator, t.value].filter(Boolean).join(' ')
                    const effect = t.effect ? `: ${t.effect}` : ''
                    const seconds = t.tolerationSeconds != null ? ` (${t.tolerationSeconds}s)` : ''
                    return (
                      <div key={i} className="bg-theme-elevated/30 rounded p-2 text-xs text-theme-text-secondary">
                        {parts}{effect}{seconds}
                      </div>
                    )
                  })}
                </div>
              </div>
            )}
          </div>
        </Section>
      )}
    </>
  )
}
