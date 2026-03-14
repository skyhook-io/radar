import { useState, useMemo } from 'react'
import { ChevronRight, ChevronDown } from 'lucide-react'
import type { LogLevel } from './useLogBuffer'
import { highlightJson, unescapeJsonStrings, parseLogfmt } from '../../utils/log-format'

interface StructuredLogLineProps {
  content: string
  level: LogLevel
  wordWrap: boolean
  isLogfmt?: boolean
  defaultExpanded?: boolean
}

export function StructuredLogLine({ content, level, wordWrap, isLogfmt, defaultExpanded }: StructuredLogLineProps) {
  const [localExpanded, setLocalExpanded] = useState<boolean | null>(null)
  const expanded = localExpanded ?? defaultExpanded ?? false

  const parsed = useMemo(() => {
    try {
      if (isLogfmt) {
        return parseLogfmt(content)
      }
      return JSON.parse(content.trim())
    } catch {
      return null
    }
  }, [content, isLogfmt])

  if (!parsed) {
    const levelColor = getLevelTextColor(level)
    return (
      <span className={`${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'} ${levelColor}`}>
        {content}
      </span>
    )
  }

  const fieldCount = Object.keys(parsed).length

  return (
    <span>
      <button
        onClick={() => setLocalExpanded(!expanded)}
        className="inline-flex items-center gap-0.5 hover:bg-theme-surface/50 rounded px-0.5 -ml-0.5 align-middle"
      >
        {expanded
          ? <ChevronDown className="w-3 h-3 shrink-0 text-theme-text-tertiary" />
          : <ChevronRight className="w-3 h-3 shrink-0 text-theme-text-tertiary" />
        }
      </button>
      {!expanded ? (
        <span className={wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'}>
          <SummaryLine obj={parsed} />
          <span className="text-theme-text-tertiary ml-1">{`{${fieldCount} fields}`}</span>
        </span>
      ) : (
        <span className={`block ml-4 ${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'}`}>
          {isLogfmt ? (
            <ExpandedLogfmt obj={parsed} />
          ) : (
            <span dangerouslySetInnerHTML={{
              __html: highlightJson(unescapeJsonStrings(formatJsonExpanded(parsed)))
            }} />
          )}
        </span>
      )}
    </span>
  )
}

function SummaryLine({ obj }: { obj: Record<string, unknown> }) {
  const lvl = obj.level ?? obj.severity ?? obj.lvl ?? nestedField(obj, 'log', 'level')
  const msg = obj.msg ?? obj.message
  const err = obj.error ?? obj.err ?? nestedField(obj, 'error', 'message')
  const caller = obj.caller ?? obj.source

  return (
    <>
      {lvl != null && (
        <span className={`${getLevelBadgeColor(lvl)} text-[10px] font-semibold px-1 py-px rounded mr-1.5 inline-block`}>
          {formatLevel(lvl)}
        </span>
      )}
      {typeof msg === 'string' && (
        <span className="text-theme-text-primary">{msg}</span>
      )}
      {typeof err === 'string' && (
        <span className="text-red-400 ml-2">error={err}</span>
      )}
      {typeof caller === 'string' && (
        <span className="text-theme-text-disabled ml-2">{caller}</span>
      )}
    </>
  )
}

function ExpandedLogfmt({ obj }: { obj: Record<string, unknown> }) {
  return (
    <>
      {Object.entries(obj).map(([key, val]) => (
        <div key={key}>
          <span style={{ color: '#7cacf8' }}>{key}</span>
          <span className="text-theme-text-tertiary">=</span>
          <span style={{ color: '#73c991' }}>{String(val)}</span>
        </div>
      ))}
    </>
  )
}

function nestedField(obj: Record<string, unknown>, parent: string, child: string): unknown {
  const p = obj[parent]
  if (p && typeof p === 'object' && !Array.isArray(p)) {
    return (p as Record<string, unknown>)[child]
  }
  return undefined
}

function formatLevel(lvl: unknown): string {
  if (typeof lvl === 'number') {
    if (lvl >= 50) return 'ERR'
    if (lvl >= 40) return 'WARN'
    if (lvl >= 30) return 'INFO'
    return 'DBG'
  }
  return String(lvl).toUpperCase()
}

function getLevelBadgeColor(lvl: unknown): string {
  const s = typeof lvl === 'number'
    ? (lvl >= 50 ? 'error' : lvl >= 40 ? 'warn' : lvl >= 30 ? 'info' : 'debug')
    : String(lvl).toLowerCase()
  if (/^(error|err|fatal|panic|critical|crit)$/.test(s)) return 'bg-red-500/20 text-red-400'
  if (/^(warn|warning)$/.test(s)) return 'bg-yellow-500/20 text-yellow-400'
  if (/^(info|information|notice)$/.test(s)) return 'bg-blue-500/20 text-blue-400'
  if (/^(debug|dbg|trace|verbose)$/.test(s)) return 'bg-gray-500/20 text-gray-400'
  return 'bg-gray-500/20 text-theme-text-secondary'
}

function getLevelTextColor(level: LogLevel): string {
  switch (level) {
    case 'error': return 'text-red-400'
    case 'warn': return 'text-yellow-400'
    case 'debug': return 'text-theme-text-secondary'
    case 'info': return 'text-theme-text-primary'
    default: return 'text-theme-text-primary'
  }
}

function formatJsonExpanded(obj: Record<string, unknown>): string {
  try {
    return JSON.stringify(obj, null, 2)
  } catch {
    return String(obj)
  }
}
