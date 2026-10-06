import { useState, useMemo } from 'react'
import { ChevronRight, ChevronDown, Filter } from 'lucide-react'
import type { LogLevel } from './useLogBuffer'
import { selectLevelField } from '../../utils/log-level'
import { unescapeJsonStrings, parseLogfmt } from '../../utils/log-format'
import { getLogPalette, getLogLevelColor, type LogPalette } from './log-palette'

interface StructuredLogLineProps {
  content: string
  level: LogLevel
  wordWrap: boolean
  isLogfmt?: boolean
  defaultExpanded?: boolean
  /**
   * When provided, field values in the expanded view become filterable — hovering
   * shows a chip that, on click, calls onFilterValue(value) so the parent can
   * add the value to the log search/filter.
   */
  onFilterValue?: (value: string) => void
  /**
   * Whether the log viewer is in dark mode. Determines the color palette used
   * for text, hover states, and level badges. Defaults to `true` since the
   * viewer defaults to dark mode.
   */
  isDark?: boolean
}

export function StructuredLogLine({ content, level, wordWrap, isLogfmt, defaultExpanded, onFilterValue, isDark = true }: StructuredLogLineProps) {
  const palette = useMemo(() => getLogPalette(isDark), [isDark])
  // null = user hasn't toggled this line; defers to defaultExpanded (global toggle)
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
    return (
      <span className={`${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'} ${getLogLevelColor(level, isDark)}`}>
        {content}
      </span>
    )
  }

  const fieldCount = Object.keys(parsed).length

  const toggle = () => setLocalExpanded(!expanded)
  const chevron = expanded
    ? <ChevronDown className={`w-3 h-3 shrink-0 ${palette.textTertiary}`} />
    : <ChevronRight className={`w-3 h-3 shrink-0 ${palette.textTertiary}`} />

  return (
    <span>
      {!expanded ? (
        // Collapsed: entire summary line is clickable
        <span
          onClick={toggle}
          className={`cursor-pointer ${palette.hoverSurface} rounded px-0.5 -ml-0.5 ${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'}`}
        >
          <span className="inline-flex items-center align-middle mr-0.5">{chevron}</span>
          <SummaryLine obj={parsed} level={level} palette={palette} />
          <span className={`${palette.textTertiary} ml-1`}>{`{${fieldCount} fields}`}</span>
        </span>
      ) : (
        // Expanded: summary header is clickable to collapse, JSON content is selectable
        <>
        <span
          onClick={toggle}
          className={`cursor-pointer ${palette.hoverSurface} rounded px-0.5 -ml-0.5`}
        >
          <span className="inline-flex items-center align-middle mr-0.5">{chevron}</span>
          <SummaryLine obj={parsed} level={level} palette={palette} />
          <span className={`${palette.textTertiary} ml-1`}>{`{${fieldCount} fields}`}</span>
        </span>
        <span className={`block ml-4 ${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'}`}>
          {isLogfmt ? (
            <ExpandedLogfmt obj={parsed} onFilterValue={onFilterValue} palette={palette} />
          ) : (
            <JsonExpanded
              text={unescapeJsonStrings(JSON.stringify(parsed, null, 2))}
              onFilterValue={onFilterValue}
              palette={palette}
            />
          )}
        </span>
        </>
      )}
    </span>
  )
}

/**
 * Render a primitive log-field value with an optional filter chip that appears on hover.
 * Clicking the chip calls onFilter(value) so the caller can push it into log search.
 */
function FilterableValue({
  value, onFilter, color, palette,
}: { value: string; onFilter?: (v: string) => void; color?: string; palette: LogPalette }) {
  if (!onFilter) {
    return <span style={color ? { color } : undefined}>{value}</span>
  }
  return (
    <span className={`group/flt inline-flex items-baseline align-baseline gap-0.5 rounded ${palette.hoverSurface}`}>
      <span style={color ? { color } : undefined}>{value}</span>
      <button
        type="button"
        onClick={(e) => { e.stopPropagation(); onFilter(value) }}
        className={`opacity-0 group-hover/flt:opacity-100 transition-opacity ${palette.textTertiary} ${palette.hoverText} px-0.5`}
        title={`Filter to lines containing "${value}"`}
        aria-label={`Filter to lines containing ${value}`}
      >
        <Filter className="w-3 h-3 inline" />
      </button>
    </span>
  )
}

/**
 * Render pretty-printed JSON with filterable primitive values while preserving
 * the layout that JSON.stringify(..., null, 2) produces. Tokenizes the string
 * and emits React nodes so the hover chip can be wired per value.
 */
function JsonExpanded({ text, onFilterValue, palette }: { text: string; onFilterValue?: (v: string) => void; palette: LogPalette }) {
  const tokenRe = /("(?:\\.|[^"\\])*")\s*:|("(?:\\.|[^"\\])*")|(-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b)|\b(true|false)\b|\b(null)\b/g
  const nodes: React.ReactNode[] = []
  let lastIndex = 0
  let match: RegExpExecArray | null
  let idx = 0
  while ((match = tokenRe.exec(text)) !== null) {
    if (match.index > lastIndex) {
      nodes.push(<span key={`t${idx++}`}>{text.slice(lastIndex, match.index)}</span>)
    }
    const [, key, str, num, bool, nil] = match
    if (key !== undefined) {
      nodes.push(<span key={`k${idx++}`} style={{ color: palette.syntaxKey }}>{key}</span>)
      nodes.push(<span key={`c${idx++}`}>:</span>)
    } else if (str !== undefined) {
      // Unescape the quoted string for the filter value (users expect to filter on
      // the displayed string, not JSON-escaped bytes).
      const inner = str.slice(1, -1).replace(/\\"/g, '"').replace(/\\\\/g, '\\')
      nodes.push(
        <FilterableValue key={`s${idx++}`} value={inner} onFilter={onFilterValue} color={palette.syntaxString} palette={palette} />
      )
    } else if (num !== undefined) {
      nodes.push(
        <FilterableValue key={`n${idx++}`} value={num} onFilter={onFilterValue} color={palette.syntaxNumber} palette={palette} />
      )
    } else if (bool !== undefined) {
      nodes.push(
        <FilterableValue key={`b${idx++}`} value={bool} onFilter={onFilterValue} color={palette.syntaxBoolean} palette={palette} />
      )
    } else if (nil !== undefined) {
      nodes.push(<span key={`z${idx++}`} style={{ color: palette.syntaxNull }}>{nil}</span>)
    }
    lastIndex = tokenRe.lastIndex
  }
  if (lastIndex < text.length) {
    nodes.push(<span key={`t${idx++}`}>{text.slice(lastIndex)}</span>)
  }
  return <>{nodes}</>
}

function SummaryLine({ obj, level, palette }: { obj: Record<string, unknown>; level: LogLevel; palette: LogPalette }) {
  const lvl = selectLevelField(obj)?.raw
  const msg = obj.msg ?? obj.message
  const rawErr = obj.error ?? obj.err
  const err = typeof rawErr === 'string'
    ? rawErr
    : nestedField(obj, 'error', 'message') ?? nestedField(obj, 'err', 'message')
  const caller = obj.caller ?? obj.source

  return (
    <>
      {lvl != null && (
        <span className={`${levelBadgeColor(level, palette)} text-[10px] font-semibold px-1 py-px rounded mr-1.5 inline-block`}>
          {formatLevel(lvl, level)}
        </span>
      )}
      {typeof msg === 'string' && (
        <span className={palette.textPrimary}>{msg}</span>
      )}
      {typeof err === 'string' && (
        <span className={`${palette.textError} ml-2`}>error={err}</span>
      )}
      {typeof caller === 'string' && (
        <span className={`${palette.textDisabled} ml-2`}>{caller}</span>
      )}
    </>
  )
}

function ExpandedLogfmt({ obj, onFilterValue, palette }: { obj: Record<string, unknown>; onFilterValue?: (v: string) => void; palette: LogPalette }) {
  return (
    <>
      {Object.entries(obj).map(([key, val]) => {
        const str = String(val)
        return (
          <div key={key}>
            <span style={{ color: palette.syntaxKey }}>{key}</span>
            <span className={palette.textTertiary}>=</span>
            <FilterableValue value={str} onFilter={onFilterValue} color={palette.syntaxString} palette={palette} />
          </div>
        )
      })}
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

const NUMERIC_LEVEL_LABELS: Record<LogLevel, string> = { error: 'ERR', warn: 'WARN', info: 'INFO', debug: 'DBG', unknown: '?' }

function formatLevel(lvl: unknown, level: LogLevel): string {
  if (typeof lvl === 'number') return NUMERIC_LEVEL_LABELS[level]
  return String(lvl).toUpperCase()
}

function levelBadgeColor(level: LogLevel, palette: LogPalette): string {
  switch (level) {
    case 'error': return palette.levelBadgeError
    case 'warn': return palette.levelBadgeWarn
    case 'info': return palette.levelBadgeInfo
    case 'debug': return palette.levelBadgeDebug
    default: return palette.levelBadgeNeutral
  }
}
