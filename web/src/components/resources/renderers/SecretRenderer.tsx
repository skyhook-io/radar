import { useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import { Section, PropertyList, Property } from '../drawer-components'

interface SecretRendererProps {
  data: any
}

export function SecretRenderer({ data }: SecretRendererProps) {
  const [revealed, setRevealed] = useState<Set<string>>(new Set())
  const dataKeys = Object.keys(data.data || {})

  const toggleReveal = (key: string) => {
    setRevealed(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const decodeBase64 = (value: string) => {
    try {
      return atob(value)
    } catch {
      return '[binary data]'
    }
  }

  return (
    <>
      <Section title="Secret">
        <PropertyList>
          <Property label="Type" value={data.type || 'Opaque'} />
          <Property label="Keys" value={String(dataKeys.length)} />
          {data.immutable && <Property label="Immutable" value="Yes" />}
        </PropertyList>
      </Section>

      <Section title="Data" defaultExpanded>
        <div className="space-y-2">
          {dataKeys.map((key) => (
            <div key={key} className="bg-theme-elevated/30 rounded p-2">
              <div className="flex items-center justify-between">
                <span className="text-sm text-theme-text-primary">{key}</span>
                <button
                  onClick={() => toggleReveal(key)}
                  className="text-xs text-theme-text-secondary hover:text-theme-text-primary"
                >
                  {revealed.has(key) ? 'Hide' : 'Reveal'}
                </button>
              </div>
              {revealed.has(key) && (
                <pre className="mt-2 bg-theme-base rounded p-2 text-xs text-theme-text-secondary overflow-x-auto max-h-40 whitespace-pre-wrap">
                  {decodeBase64(data.data[key])}
                </pre>
              )}
            </div>
          ))}
          {dataKeys.length === 0 && (
            <div className="text-sm text-theme-text-tertiary">No data</div>
          )}
        </div>
      </Section>

      <div className="flex items-center gap-2 p-3 bg-red-500/10 border border-red-500/30 rounded text-red-400 text-sm">
        <AlertTriangle className="w-4 h-4" />
        Secret values are sensitive. Be careful when revealing.
      </div>
    </>
  )
}
