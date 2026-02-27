import { useState, useRef, useEffect } from 'react'
import { AlertTriangle, Copy, Check } from 'lucide-react'
import { Section, ExpandableSection } from '../drawer-components'
import { Tooltip } from '../../ui/Tooltip'
import { formatBytes } from '../resource-utils'

interface ConfigMapRendererProps {
  data: any
}

// Short = single-line and under 120 chars — render inline
function isShortValue(value: string): boolean {
  return !value.includes('\n') && value.length <= 120
}

function TruncatedKey({ text }: { text: string }) {
  const ref = useRef<HTMLSpanElement>(null)
  const [isTruncated, setIsTruncated] = useState(false)

  useEffect(() => {
    const el = ref.current
    if (el) setIsTruncated(el.scrollWidth > el.clientWidth)
  }, [text])

  return (
    <Tooltip content={text} delay={100} position="top" disabled={!isTruncated}>
      <span ref={ref} className="truncate block">{text}</span>
    </Tooltip>
  )
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => {
        navigator.clipboard.writeText(text)
        setCopied(true)
        setTimeout(() => setCopied(false), 1500)
      }}
      className="p-0.5 text-theme-text-tertiary hover:text-theme-text-primary opacity-0 group-hover/row:opacity-100 transition-opacity shrink-0"
      title="Copy value"
    >
      {copied ? <Check className="w-3 h-3 text-green-400" /> : <Copy className="w-3 h-3" />}
    </button>
  )
}

export function ConfigMapRenderer({ data }: ConfigMapRendererProps) {
  const dataEntries = Object.entries((data.data || {}) as Record<string, string>)
  const binaryDataKeys = Object.keys(data.binaryData || {})

  const shortEntries = dataEntries.filter(([, v]) => isShortValue(v))
  const longEntries = dataEntries.filter(([, v]) => !isShortValue(v))

  // Dynamic value column width: give values as much room as they need (30-55%)
  const maxValueLen = shortEntries.reduce((max, [, v]) => Math.max(max, v.length), 0)
  // Short values (< 10 chars like "true", "5m") → values get 30%, keys get 70%
  // Long values (URLs, addresses) → values get up to 55%
  const valuePercent = Math.min(55, Math.max(30, Math.round(maxValueLen * 1.5 + 10)))
  const keyPercent = 100 - valuePercent

  return (
    <>
      <Section title={`Data (${dataEntries.length + binaryDataKeys.length} keys)`} defaultExpanded>
        <div className="space-y-3">
          {/* Short values: compact table */}
          {shortEntries.length > 0 && (
            <table className="w-full bg-theme-base rounded overflow-hidden text-xs border-separate border-spacing-0 table-fixed">
              <colgroup>
                <col style={{ width: `${keyPercent}%` }} />
                <col style={{ width: `${valuePercent}%` }} />
                <col style={{ width: '20px' }} />
              </colgroup>
              <tbody>
                {shortEntries.map(([key, value], i) => (
                  <tr key={key} className={`group/row ${i % 2 === 0 ? '' : 'bg-theme-elevated/30'}`}>
                    <td className="px-2.5 py-1.5 text-theme-text-secondary font-medium truncate align-top">
                      <TruncatedKey text={key} />
                    </td>
                    <td className="px-2.5 py-1.5 text-theme-text-primary font-mono break-all align-top">
                      {value}
                    </td>
                    <td className="py-1.5 pr-2 align-top">
                      <CopyButton text={value} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {/* Long/multiline values: expandable code blocks */}
          {longEntries.map(([key, value]) => {
            const isLarge = value.length > 500
            return (
              <ExpandableSection key={key} title={`${key} (${formatBytes(value.length)})`} defaultExpanded={!isLarge}>
                <pre className="bg-theme-base rounded p-2 text-xs text-theme-text-secondary overflow-x-auto max-h-60 whitespace-pre-wrap">
                  {value}
                </pre>
              </ExpandableSection>
            )
          })}

          {binaryDataKeys.map((key) => (
            <div key={key} className="text-sm text-theme-text-secondary">
              {key} <span className="text-xs">(binary)</span>
            </div>
          ))}
          {dataEntries.length === 0 && binaryDataKeys.length === 0 && (
            <div className="text-sm text-theme-text-tertiary">No data</div>
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
