import { useState, useMemo } from 'react'
import { ChevronRight, ChevronDown } from 'lucide-react'
import { getLevelColor } from '../../utils/log-format'
import type { LogLevel } from './useLogBuffer'

interface JsonLogLineProps {
  content: string
  level: LogLevel
  wordWrap: boolean
}

export function JsonLogLine({ content, level, wordWrap }: JsonLogLineProps) {
  const [expanded, setExpanded] = useState(false)
  const levelColor = getLevelColor(level)

  const parsed = useMemo(() => {
    try {
      return JSON.parse(content.trim())
    } catch {
      return null
    }
  }, [content])

  if (!parsed) {
    return (
      <span className={`${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'} ${levelColor}`}>
        {content}
      </span>
    )
  }

  const summary = buildSummary(parsed)
  const fieldCount = Object.keys(parsed).length

  return (
    <span className={levelColor}>
      <button
        onClick={() => setExpanded(!expanded)}
        className="inline-flex items-center gap-0.5 hover:bg-theme-surface/50 rounded px-0.5 -ml-0.5 align-top"
      >
        {expanded
          ? <ChevronDown className="w-3 h-3 shrink-0 text-theme-text-tertiary" />
          : <ChevronRight className="w-3 h-3 shrink-0 text-theme-text-tertiary" />
        }
      </button>
      {!expanded ? (
        <span className={wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'}>
          {summary}
          <span className="text-theme-text-tertiary ml-1">{`{${fieldCount} fields}`}</span>
        </span>
      ) : (
        <span className={`block ml-4 ${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'}`}>
          {formatJsonExpanded(parsed)}
        </span>
      )}
    </span>
  )
}

function buildSummary(obj: Record<string, unknown>): string {
  const parts: string[] = []

  // Level
  const lvl = obj.level || obj.severity || obj.lvl
  if (typeof lvl === 'string') {
    parts.push(lvl.toUpperCase())
  }

  // Message
  const msg = obj.msg || obj.message
  if (typeof msg === 'string') {
    parts.push(msg)
  }

  // Error
  const err = obj.error || obj.err
  if (typeof err === 'string') {
    parts.push(`error=${err}`)
  }

  return parts.join('  ')
}

function formatJsonExpanded(obj: Record<string, unknown>): string {
  try {
    return JSON.stringify(obj, null, 2)
  } catch {
    return String(obj)
  }
}
