import { ArrowUpDown } from 'lucide-react'
import { Section, PropertyList, Property } from '../drawer-components'

interface PriorityClassRendererProps {
  data: any
}

export function PriorityClassRenderer({ data }: PriorityClassRendererProps) {
  const preemptionPolicy = data.preemptionPolicy || 'PreemptLowerPriority'

  return (
    <>
      <Section title="Priority Class" icon={ArrowUpDown}>
        <PropertyList>
          <Property label="Value" value={data.value != null ? String(data.value) : undefined} />
          <Property
            label="Global Default"
            value={
              data.globalDefault != null ? (
                <span className={data.globalDefault ? 'text-green-400' : 'text-theme-text-tertiary'}>
                  {data.globalDefault ? 'Yes' : 'No'}
                </span>
              ) : (
                <span className="text-theme-text-tertiary">No</span>
              )
            }
          />
          <Property
            label="Preemption Policy"
            value={
              <span
                className={preemptionPolicy === 'Never' ? 'text-green-400' : 'text-amber-400'}
                title={
                  preemptionPolicy === 'Never'
                    ? 'Will wait in queue instead of preempting'
                    : 'Will preempt running pods with lower priority'
                }
              >
                {preemptionPolicy}
              </span>
            }
          />
        </PropertyList>
        {data.description && (
          <p className="mt-3 text-sm text-theme-text-tertiary leading-relaxed">{data.description}</p>
        )}
      </Section>
    </>
  )
}
