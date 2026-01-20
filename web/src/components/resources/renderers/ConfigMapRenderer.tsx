import { AlertTriangle } from 'lucide-react'
import { Section, ExpandableSection } from '../drawer-components'
import { formatBytes } from '../resource-utils'

interface ConfigMapRendererProps {
  data: any
}

export function ConfigMapRenderer({ data }: ConfigMapRendererProps) {
  const dataKeys = Object.keys(data.data || {})
  const binaryDataKeys = Object.keys(data.binaryData || {})

  return (
    <>
      <Section title="Data" defaultExpanded>
        <div className="space-y-2">
          {dataKeys.map((key) => {
            const value = data.data[key]
            const isLarge = value.length > 500
            return (
              <ExpandableSection key={key} title={`${key} (${formatBytes(value.length)})`} defaultExpanded={!isLarge}>
                <pre className="bg-slate-900 rounded p-2 text-xs text-slate-300 overflow-x-auto max-h-60 whitespace-pre-wrap">
                  {value}
                </pre>
              </ExpandableSection>
            )
          })}
          {binaryDataKeys.map((key) => (
            <div key={key} className="text-sm text-slate-400">
              {key} <span className="text-xs">(binary)</span>
            </div>
          ))}
          {dataKeys.length === 0 && binaryDataKeys.length === 0 && (
            <div className="text-sm text-slate-500">No data</div>
          )}
        </div>
      </Section>

      {data.immutable && (
        <div className="flex items-center gap-2 p-3 bg-yellow-500/10 border border-yellow-500/30 rounded text-yellow-400 text-sm">
          <AlertTriangle className="w-4 h-4" />
          Immutable ConfigMap
        </div>
      )}
    </>
  )
}
