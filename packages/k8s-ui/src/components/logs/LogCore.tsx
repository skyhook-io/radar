import { useRef, useCallback, useState, useMemo, useEffect, type ReactNode } from 'react'
import { Virtuoso, type VirtuosoHandle } from 'react-virtuoso'
import { Play, Square, Download, Search, X, Terminal, RotateCcw, ChevronUp, ChevronDown, ChevronRight, CaseSensitive, Regex, WrapText, Clock, Copy, Trash2, Filter, Braces, Palette, ListCollapse, Sun, Moon } from 'lucide-react'
import type { LogEntry, LogLevel } from './useLogBuffer'
import { useLogSearch } from './useLogSearch'
import { StructuredLogLine } from './StructuredLogLine'
import { Tooltip } from '../ui/Tooltip'
import {
  formatLogTimestamp,
  highlightSearchMatches,
  stripAnsi,
  ansiToHtml,
  type TimestampFormat,
  TIMESTAMP_FORMAT_LABELS,
} from '../../utils/log-format'
import { getLogPalette, getLogLevelColor, type LogPalette } from './log-palette'

export type DownloadFormat = 'txt' | 'json' | 'csv'

/**
 * `toolbarExtra` may be a plain ReactNode, or a function that receives the
 * current dark/light context so wrappers can produce palette-matched controls.
 */
export type ToolbarExtraRenderer =
  | ReactNode
  | ((ctx: { isDark: boolean; palette: LogPalette }) => ReactNode)

interface LogCoreProps {
  entries: LogEntry[]
  isLoading: boolean
  isStreaming: boolean
  onStartStream?: () => void
  onStopStream: () => void
  onRefresh: () => void
  onDownload: (format: DownloadFormat) => void
  onClear?: () => void
  toolbarExtra?: ToolbarExtraRenderer
  showPodName?: boolean
  emptyMessage?: string
  errorMessage?: string | null
  /**
   * Hard override for the viewer palette. When set, the viewer stays pinned to
   * that mode and hides the in-viewer dark/light toggle. When undefined,
   * the viewer manages its own palette via localStorage and the Sun/Moon button.
   */
  forceDark?: boolean
}

interface LevelOption {
  level: LogLevel
  label: string
}

const LEVEL_OPTIONS: LevelOption[] = [
  { level: 'error', label: 'ERR' },
  { level: 'warn', label: 'WARN' },
  { level: 'info', label: 'INFO' },
  { level: 'debug', label: 'DBG' },
]

function getLevelActiveColor(level: LogLevel, palette: LogPalette): string {
  switch (level) {
    case 'error': return palette.levelActiveError
    case 'warn': return palette.levelActiveWarn
    case 'info': return palette.levelActiveInfo
    case 'debug': return palette.levelActiveDebug
    default: return palette.levelActiveDebug
  }
}

const TIMESTAMP_FORMAT_ORDER: TimestampFormat[] = [
  'time-local', 'time-utc', 'iso-local', 'iso-utc', 'relative', 'epoch',
]

const TIMESTAMP_FORMAT_SHORT_LABELS: Record<TimestampFormat, string> = {
  'time-local': 'Local time',
  'time-utc': 'UTC time',
  'iso-local': 'Full date',
  'iso-utc': 'UTC date',
  'relative': 'Relative',
  'epoch': 'Unix time',
}

function isContinuationLine(content: string): boolean {
  // Lines starting with whitespace are the dominant stack-trace continuation pattern:
  // Java `\tat com.foo.Bar`, Go `\tpackage.func`, Node `    at func`, Python `  File "..."`.
  if (/^\s/.test(content)) return true
  // Java's secondary chain markers that don't start with whitespace.
  return /^(Caused by:|Suppressed:|\.\.\. \d+ more)/.test(content)
}

interface LogGroup {
  head: LogEntry
  continuations: LogEntry[]
}

const TIP_DELAY = 150

export function LogCore({
  entries,
  isLoading,
  isStreaming,
  onStartStream,
  onStopStream,
  onRefresh,
  onDownload,
  onClear,
  toolbarExtra,
  showPodName = false,
  emptyMessage = 'No logs available',
  errorMessage,
  forceDark,
}: LogCoreProps) {
  const virtuosoRef = useRef<VirtuosoHandle>(null)
  const [atBottom, setAtBottom] = useState(true)
  const themeLocked = typeof forceDark === 'boolean'
  // Seed isDark: forceDark prop wins; else localStorage['radar-logs-dark'];
  // else default dark. See log-palette.ts for why the viewer is palette-driven
  // instead of theme-token-driven.
  const [isDark, setIsDark] = useState<boolean>(() => {
    if (typeof forceDark === 'boolean') return forceDark
    try {
      const v = localStorage.getItem('radar-logs-dark')
      if (v === 'false') return false
      if (v === 'true') return true
    } catch {}
    return true
  })
  useEffect(() => {
    if (typeof forceDark === 'boolean') {
      setIsDark(forceDark)
    }
  }, [forceDark])
  const palette = useMemo(() => getLogPalette(isDark), [isDark])
  const toggleDark = useCallback(() => {
    if (themeLocked) return
    setIsDark(prev => {
      const next = !prev
      try { localStorage.setItem('radar-logs-dark', String(next)) } catch {}
      return next
    })
  }, [themeLocked])
  const [wordWrap, setWordWrap] = useState(() => {
    try { return localStorage.getItem('radar-logs-wrap') !== 'false' } catch { return true }
  })
  const [showTimestamps, setShowTimestamps] = useState(() => {
    try { return localStorage.getItem('radar-logs-timestamps') !== 'false' } catch { return true }
  })
  const [tsFormat, setTsFormat] = useState<TimestampFormat>(() => {
    try {
      const v = localStorage.getItem('radar-logs-ts-format') as TimestampFormat | null
      return v && TIMESTAMP_FORMAT_ORDER.includes(v) ? v : 'time-local'
    } catch { return 'time-local' }
  })
  const [ansiEnabled, setAnsiEnabled] = useState(() => {
    try { return localStorage.getItem('radar-logs-ansi') !== 'false' } catch { return true }
  })
  const [collapseStacks, setCollapseStacks] = useState(() => {
    try { return localStorage.getItem('radar-logs-collapse-stacks') !== 'false' } catch { return true }
  })
  const [enabledLevels, setEnabledLevels] = useState<Set<LogLevel>>(
    new Set(['error', 'warn', 'info', 'debug'])
  )
  const [showDownloadMenu, setShowDownloadMenu] = useState(false)
  const [showTsMenu, setShowTsMenu] = useState(false)
  const [expandAllStructured, setExpandAllStructured] = useState(false)
  const [expandedStacks, setExpandedStacks] = useState<Set<number>>(() => new Set())

  // Re-render every 15s so "relative" timestamps tick forward during idle viewing.
  const [, setNowTick] = useState(0)
  useEffect(() => {
    if (tsFormat !== 'relative' || !showTimestamps) return
    const id = setInterval(() => setNowTick(n => n + 1), 15_000)
    return () => clearInterval(id)
  }, [tsFormat, showTimestamps])

  // Level-filtered entries
  // 'unknown' logs are shown when all 4 known levels are enabled (no active filtering)
  const levelFilteredEntries = useMemo(() => {
    const allEnabled = LEVEL_OPTIONS.every(opt => enabledLevels.has(opt.level))
    if (allEnabled) return entries
    return entries.filter(e => enabledLevels.has(e.level))
  }, [entries, enabledLevels])

  // Level counts for badges
  const levelCounts = useMemo(() => {
    const counts: Record<LogLevel, number> = { error: 0, warn: 0, info: 0, debug: 0, unknown: 0 }
    for (const e of entries) {
      counts[e.level]++
    }
    return counts
  }, [entries])

  const hasStructuredEntries = useMemo(() => entries.some(e => e.isJson || e.isLogfmt), [entries])

  // Search
  const search = useLogSearch(levelFilteredEntries, virtuosoRef)

  // Display entries: use search-filtered when filter mode is active
  const displayEntries = search.isFilterMode && search.query
    ? search.filteredEntries
    : levelFilteredEntries

  // Close download menu on next click anywhere (deferred so current click doesn't trigger it)
  const downloadMenuRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!showDownloadMenu) return
    const handleClick = (e: MouseEvent) => {
      if (downloadMenuRef.current?.contains(e.target as Node)) return
      setShowDownloadMenu(false)
    }
    window.addEventListener('click', handleClick)
    return () => window.removeEventListener('click', handleClick)
  }, [showDownloadMenu])

  // Same close-on-outside-click for the timestamp format menu.
  const tsMenuRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!showTsMenu) return
    const handleClick = (e: MouseEvent) => {
      if (tsMenuRef.current?.contains(e.target as Node)) return
      setShowTsMenu(false)
    }
    window.addEventListener('click', handleClick)
    return () => window.removeEventListener('click', handleClick)
  }, [showTsMenu])

  // Keyboard shortcut: Ctrl+F to open search
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'f') {
        e.preventDefault()
        search.open()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [search.open])

  const handleFollowOutput = useCallback((isAtBottom: boolean) => {
    if (isAtBottom) return 'smooth' as const
    return false as const
  }, [])

  const handleAtBottomStateChange = useCallback((bottom: boolean) => {
    setAtBottom(bottom)
  }, [])

  const toggleWrap = useCallback(() => {
    setWordWrap(prev => {
      const next = !prev
      try { localStorage.setItem('radar-logs-wrap', String(next)) } catch {}
      return next
    })
  }, [])

  const toggleTimestamps = useCallback(() => {
    setShowTimestamps(prev => {
      const next = !prev
      try { localStorage.setItem('radar-logs-timestamps', String(next)) } catch {}
      return next
    })
  }, [])

  const pickTsFormat = useCallback((fmt: TimestampFormat) => {
    setTsFormat(fmt)
    try { localStorage.setItem('radar-logs-ts-format', fmt) } catch {}
    // Auto-show timestamps when user picks a format — otherwise the change isn't visible.
    setShowTimestamps(true)
    try { localStorage.setItem('radar-logs-timestamps', 'true') } catch {}
    setShowTsMenu(false)
  }, [])

  const toggleAnsi = useCallback(() => {
    setAnsiEnabled(prev => {
      const next = !prev
      try { localStorage.setItem('radar-logs-ansi', String(next)) } catch {}
      return next
    })
  }, [])

  const toggleCollapseStacks = useCallback(() => {
    setCollapseStacks(prev => {
      const next = !prev
      try { localStorage.setItem('radar-logs-collapse-stacks', String(next)) } catch {}
      return next
    })
  }, [])

  const toggleStackExpanded = useCallback((id: number) => {
    setExpandedStacks(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  // Clicking a filter chip in a structured log value pushes the value into the
  // log search and enables filter mode so only matching lines are shown.
  const handleFilterValue = useCallback((value: string) => {
    search.setQuery(value)
    // Values often contain regex metacharacters — force literal-substring matching.
    search.setIsRegex(false)
    search.setFilterMode(true)
    if (!search.isOpen) search.open()
  }, [search])

  const toggleLevel = useCallback((level: LogLevel) => {
    setEnabledLevels(prev => {
      const next = new Set(prev)
      if (next.has(level)) {
        next.delete(level)
      } else {
        next.add(level)
      }
      return next
    })
  }, [])

  // Highlight set for current match
  const currentHighlightId = search.matchIndices.length > 0
    ? (search.isFilterMode
        ? search.filteredEntries[search.currentMatch]?.id
        : levelFilteredEntries[search.matchIndices[search.currentMatch]]?.id)
    : -1

  // Group stack-trace continuation lines under their preceding head line.
  // Disabled while search is active so matches inside continuations remain visible.
  const groupedEntries = useMemo<LogGroup[]>(() => {
    if (!collapseStacks || search.query) {
      return displayEntries.map(e => ({ head: e, continuations: [] }))
    }
    const groups: LogGroup[] = []
    for (const entry of displayEntries) {
      if (groups.length > 0 && isContinuationLine(entry.content)) {
        groups[groups.length - 1].continuations.push(entry)
      } else {
        groups.push({ head: entry, continuations: [] })
      }
    }
    return groups
  }, [displayEntries, collapseStacks, search.query])

  const scrollToBottom = useCallback(() => {
    virtuosoRef.current?.scrollToIndex({
      index: groupedEntries.length - 1,
      align: 'end',
      behavior: 'smooth',
    })
  }, [groupedEntries.length])

  const toolbarExtraNode = typeof toolbarExtra === 'function'
    ? toolbarExtra({ isDark, palette })
    : toolbarExtra

  // Composite "inactive toolbar button" classes — static literals so
  // Tailwind's class scanner picks them up. Two variants for secondary
  // vs tertiary default text color.
  const iconBtnInactive = isDark
    ? 'p-1.5 rounded transition-colors text-slate-400 hover:text-slate-100 hover:bg-slate-800'
    : 'p-1.5 rounded transition-colors text-slate-600 hover:text-slate-900 hover:bg-slate-200'
  const iconBtnInactiveTertiary = isDark
    ? 'p-1.5 rounded transition-colors text-slate-500 hover:text-slate-100 hover:bg-slate-800'
    : 'p-1.5 rounded transition-colors text-slate-400 hover:text-slate-900 hover:bg-slate-200'
  // "disabled → tertiary on hover" for inactive level filter chips.
  const levelChipInactive = isDark
    ? 'border-transparent text-slate-600 hover:text-slate-500'
    : 'border-transparent text-slate-300 hover:text-slate-400'

  return (
    <div
      className={`flex flex-col h-full ${palette.containerBg}`}
      style={{ colorScheme: isDark ? 'dark' : 'light', fontFamily: "'SF Mono', 'Cascadia Code', 'Fira Code', Menlo, Consolas, 'DejaVu Sans Mono', monospace" }}
    >
      {/* Toolbar */}
      <div className={`flex items-center gap-2 px-3 py-2 border-b ${palette.border} ${palette.toolbarBg}`}>
        {toolbarExtraNode}

        {/* Stream / Stop toggle — only shown when streaming is supported */}
        {onStartStream && (
          <Tooltip content={isStreaming ? 'Stop streaming' : 'Start streaming'} delay={TIP_DELAY} position="bottom">
            <button
              onClick={isStreaming ? onStopStream : onStartStream}
              className={`flex items-center gap-1.5 px-2 py-1.5 text-xs rounded transition-colors ${
                isStreaming
                  ? 'bg-green-600 text-white hover:bg-green-700'
                  : `${palette.elevatedBg} ${palette.textSecondary} ${palette.hoverBg}`
              }`}
            >
              {isStreaming ? <Square className="w-3 h-3" /> : <Play className="w-3 h-3" />}
              <span className="hidden sm:inline">{isStreaming ? 'Stop' : 'Stream'}</span>
            </button>
          </Tooltip>
        )}

        {/* Refresh */}
        <Tooltip content="Refresh logs" delay={TIP_DELAY} position="bottom">
          <button
            onClick={onRefresh}
            disabled={isLoading || isStreaming}
            className={`flex items-center gap-1.5 px-2 py-1.5 text-xs rounded ${palette.elevatedBg} ${palette.textSecondary} ${palette.hoverBg} disabled:opacity-50 disabled:cursor-not-allowed`}
          >
            <RotateCcw className={`w-3 h-3 ${isLoading ? 'animate-spin' : ''}`} />
          </button>
        </Tooltip>

        {/* Level filter toggles */}
        <div className="flex items-center gap-1 ml-1">
          {LEVEL_OPTIONS.map(opt => {
            const active = enabledLevels.has(opt.level)
            const count = levelCounts[opt.level]
            return (
              <Tooltip key={opt.level} content={`${active ? 'Hide' : 'Show'} ${opt.label} logs`} delay={TIP_DELAY} position="bottom">
                <button
                  onClick={() => toggleLevel(opt.level)}
                  className={`px-1.5 py-0.5 text-[10px] font-medium rounded border transition-colors ${
                    active
                      ? getLevelActiveColor(opt.level, palette)
                      : levelChipInactive
                  }`}
                >
                  {opt.label}{count > 0 ? ` ${count}` : ''}
                </button>
              </Tooltip>
            )
          })}
        </div>

        <div className="flex-1" />

        {/* Expand all structured logs toggle */}
        {hasStructuredEntries && (
          <Tooltip content={expandAllStructured ? 'Collapse all structured' : 'Expand all structured'} delay={TIP_DELAY} position="bottom">
            <button
              onClick={() => setExpandAllStructured(prev => !prev)}
              className={`p-1.5 rounded transition-colors ${
                expandAllStructured ? palette.toolbarActive : iconBtnInactive
              }`}
            >
              <Braces className="w-4 h-4" />
            </button>
          </Tooltip>
        )}

        {/* Timestamp toggle + format picker */}
        <div className="flex items-center">
          <Tooltip content={showTimestamps ? 'Hide timestamps' : 'Show timestamps'} delay={TIP_DELAY} position="bottom">
            <button
              onClick={toggleTimestamps}
              className={`p-1.5 rounded-l transition-colors ${
                showTimestamps ? palette.toolbarActive : iconBtnInactive
              }`}
            >
              <Clock className="w-4 h-4" />
            </button>
          </Tooltip>
          <div className="relative" ref={tsMenuRef}>
            <Tooltip content={`Timestamp format: ${TIMESTAMP_FORMAT_LABELS[tsFormat]}`} delay={TIP_DELAY} position="bottom">
              <button
                onClick={() => setShowTsMenu(prev => !prev)}
                className={`px-2 py-1.5 rounded-r text-[10px] font-medium transition-colors whitespace-nowrap ${
                  showTimestamps ? palette.toolbarActive : iconBtnInactiveTertiary
                }`}
                aria-label="Pick timestamp format"
              >
                <span className="inline-flex items-center gap-1">
                  <span>{TIMESTAMP_FORMAT_SHORT_LABELS[tsFormat]}</span>
                  <ChevronDown className="w-3 h-3" />
                </span>
              </button>
            </Tooltip>
            {showTsMenu && (
              <div className={`absolute top-full right-0 mt-1 w-44 ${palette.menuBg} border ${palette.border} rounded-lg shadow-lg z-50`}>
                <div className={`px-3 py-1.5 text-[10px] uppercase tracking-wide ${palette.textTertiary} border-b ${palette.border}`}>
                  Timestamp format
                </div>
                {TIMESTAMP_FORMAT_ORDER.map(fmt => (
                  <button
                    key={fmt}
                    onClick={() => pickTsFormat(fmt)}
                    className={`w-full text-left px-3 py-1.5 text-xs ${palette.hoverBg} flex items-center justify-between ${
                      tsFormat === fmt ? palette.textPrimary : palette.textSecondary
                    }`}
                  >
                    <span>{TIMESTAMP_FORMAT_LABELS[fmt]}</span>
                    {tsFormat === fmt && <span className={`text-[10px] ${palette.textAccent}`}>✓</span>}
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>

        {/* Collapse stack-trace continuations toggle */}
        <Tooltip content={collapseStacks ? 'Stop grouping stack traces' : 'Group stack-trace lines'} delay={TIP_DELAY} position="bottom">
          <button
            onClick={toggleCollapseStacks}
            className={`p-1.5 rounded transition-colors ${
              collapseStacks ? palette.toolbarActive : iconBtnInactive
            }`}
          >
            <ListCollapse className="w-4 h-4" />
          </button>
        </Tooltip>

        {/* ANSI color rendering toggle */}
        <Tooltip content={ansiEnabled ? 'Hide ANSI colors' : 'Render ANSI colors'} delay={TIP_DELAY} position="bottom">
          <button
            onClick={toggleAnsi}
            className={`p-1.5 rounded transition-colors ${
              ansiEnabled ? palette.toolbarActive : iconBtnInactive
            }`}
          >
            <Palette className="w-4 h-4" />
          </button>
        </Tooltip>

        {/* Wrap toggle */}
        <Tooltip content={wordWrap ? 'Disable word wrap' : 'Enable word wrap'} delay={TIP_DELAY} position="bottom">
          <button
            onClick={toggleWrap}
            className={`p-1.5 rounded transition-colors ${
              wordWrap ? palette.toolbarActive : iconBtnInactive
            }`}
          >
            <WrapText className="w-4 h-4" />
          </button>
        </Tooltip>

        {/* Search toggle */}
        <Tooltip content="Search (Ctrl+F)" delay={TIP_DELAY} position="bottom">
          <button
            onClick={() => search.isOpen ? search.close() : search.open()}
            className={`p-1.5 rounded transition-colors ${
              search.isOpen ? palette.toolbarActive : iconBtnInactive
            }`}
          >
            <Search className="w-4 h-4" />
          </button>
        </Tooltip>

        {!themeLocked && (
          <Tooltip content={isDark ? 'Switch to light mode' : 'Switch to dark mode'} delay={TIP_DELAY} position="bottom">
            <button
              onClick={toggleDark}
              className={iconBtnInactive}
              aria-label={isDark ? 'Switch log viewer to light mode' : 'Switch log viewer to dark mode'}
            >
              {isDark ? <Sun className="w-4 h-4" /> : <Moon className="w-4 h-4" />}
            </button>
          </Tooltip>
        )}

        {/* Download */}
        <div className="relative flex items-center" ref={downloadMenuRef}>
          <Tooltip content="Download logs" delay={TIP_DELAY} position="bottom">
            <button
              onClick={() => setShowDownloadMenu(prev => !prev)}
              className={iconBtnInactive}
            >
              <Download className="w-4 h-4" />
            </button>
          </Tooltip>
          {showDownloadMenu && (
            <div className={`absolute top-full right-0 mt-1 w-32 ${palette.menuBg} border ${palette.border} rounded-lg shadow-lg z-50`}>
              {(['txt', 'json', 'csv'] as DownloadFormat[]).map(fmt => (
                <button
                  key={fmt}
                  onClick={() => { onDownload(fmt); setShowDownloadMenu(false) }}
                  className={`w-full text-left px-3 py-2 text-xs ${palette.textPrimary} ${palette.hoverBg} first:rounded-t-lg last:rounded-b-lg`}
                >
                  {fmt.toUpperCase()}
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Clear */}
        {onClear && (
          <Tooltip content="Clear logs" delay={TIP_DELAY} position="bottom">
            <button
              onClick={onClear}
              className={iconBtnInactive}
            >
              <Trash2 className="w-4 h-4" />
            </button>
          </Tooltip>
        )}
      </div>

      {/* Search bar */}
      {search.isOpen && (
        <div className={`flex items-center gap-2 px-3 py-2 border-b ${palette.border} ${palette.toolbarBgMuted}`}>
          <Search className={`w-4 h-4 ${palette.textSecondary} shrink-0`} />
          <input
            type="text"
            value={search.query}
            onChange={(e) => search.setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                search.close()
              } else if (e.key === 'Enter') {
                if (e.shiftKey) {
                  search.goToPrev()
                } else {
                  search.goToNext()
                }
              }
            }}
            placeholder="Search logs..."
            className={`flex-1 bg-transparent ${palette.textPrimary} text-sm ${palette.placeholder} focus:outline-none min-w-0`}
            autoFocus
          />

          {/* Regex toggle */}
          <Tooltip content="Regex" delay={TIP_DELAY} position="bottom">
            <button
              onClick={search.toggleRegex}
              className={`p-1 rounded transition-colors ${
                search.isRegex ? palette.toolbarActive : `${palette.textTertiary} ${palette.hoverText}`
              }`}
            >
              <Regex className="w-3.5 h-3.5" />
            </button>
          </Tooltip>

          {/* Case sensitivity toggle */}
          <Tooltip content="Match case" delay={TIP_DELAY} position="bottom">
            <button
              onClick={search.toggleCaseSensitive}
              className={`p-1 rounded transition-colors ${
                search.isCaseSensitive ? palette.toolbarActive : `${palette.textTertiary} ${palette.hoverText}`
              }`}
            >
              <CaseSensitive className="w-3.5 h-3.5" />
            </button>
          </Tooltip>

          {/* Filter mode toggle */}
          <Tooltip content={search.isFilterMode ? 'Highlight mode' : 'Filter mode'} delay={TIP_DELAY} position="bottom">
            <button
              onClick={search.toggleFilterMode}
              className={`p-1 rounded transition-colors ${
                search.isFilterMode ? palette.toolbarActive : `${palette.textTertiary} ${palette.hoverText}`
              }`}
            >
              <Filter className="w-3.5 h-3.5" />
            </button>
          </Tooltip>

          {search.query && (
            <>
              <span className={`text-xs whitespace-nowrap ${search.regexError ? palette.textError : palette.textTertiary}`}>
                {search.regexError
                  ? 'Invalid regex'
                  : search.matchCount > 0
                    ? `${search.currentMatch + 1} / ${search.matchCount}`
                    : '0 results'}
              </span>

              {/* Navigation arrows */}
              <Tooltip content="Previous (Shift+Enter)" delay={TIP_DELAY} position="bottom">
                <button
                  onClick={search.goToPrev}
                  disabled={search.matchCount === 0}
                  className={`p-1 rounded ${palette.textSecondary} ${palette.hoverText} disabled:opacity-30`}
                >
                  <ChevronUp className="w-3.5 h-3.5" />
                </button>
              </Tooltip>
              <Tooltip content="Next (Enter)" delay={TIP_DELAY} position="bottom">
                <button
                  onClick={search.goToNext}
                  disabled={search.matchCount === 0}
                  className={`p-1 rounded ${palette.textSecondary} ${palette.hoverText} disabled:opacity-30`}
                >
                  <ChevronDown className="w-3.5 h-3.5" />
                </button>
              </Tooltip>

              <button
                onClick={() => search.setQuery('')}
                className={`p-1 rounded ${palette.textSecondary} ${palette.hoverText}`}
              >
                <X className="w-3 h-3" />
              </button>
            </>
          )}
        </div>
      )}

      {/* Log content */}
      {isLoading && entries.length === 0 ? (
        <div className={`flex-1 flex items-center justify-center ${palette.textTertiary}`}>
          <div className="flex items-center gap-2">
            <RotateCcw className="w-4 h-4 animate-spin" />
            <span>Loading logs...</span>
          </div>
        </div>
      ) : errorMessage ? (
        <div className={`flex-1 flex flex-col items-center justify-center gap-2 ${palette.textError}`}>
          <Terminal className="w-8 h-8" />
          <span>{errorMessage}</span>
        </div>
      ) : groupedEntries.length === 0 ? (
        <div className={`flex-1 flex flex-col items-center justify-center gap-2 ${palette.textTertiary}`}>
          <Terminal className="w-8 h-8" />
          <span>{emptyMessage}</span>
        </div>
      ) : (
        <div className="flex-1 relative">
          <Virtuoso
            ref={virtuosoRef}
            data={groupedEntries}
            followOutput={handleFollowOutput}
            initialTopMostItemIndex={groupedEntries.length - 1}
            atBottomStateChange={handleAtBottomStateChange}
            atBottomThreshold={50}
            increaseViewportBy={200}
            itemContent={(_index, group) => (
              <LogGroupItem
                group={group}
                searchQuery={search.query}
                searchIsRegex={search.isRegex}
                searchIsCaseSensitive={search.isCaseSensitive}
                showPodName={showPodName}
                showTimestamp={showTimestamps}
                tsFormat={tsFormat}
                ansiEnabled={ansiEnabled}
                isCurrentMatch={group.head.id === currentHighlightId}
                wordWrap={wordWrap}
                defaultExpanded={expandAllStructured}
                onFilterValue={handleFilterValue}
                isStackExpanded={expandedStacks.has(group.head.id)}
                onToggleStack={toggleStackExpanded}
                isDark={isDark}
                palette={palette}
              />
            )}
            className="h-full font-mono text-xs"
          />
          {/* Scroll-to-bottom button */}
          {!atBottom && (
            <button
              onClick={scrollToBottom}
              className="absolute bottom-4 right-14 px-3 py-1.5 btn-brand text-xs rounded-full shadow-lg z-10"
            >
              Scroll to bottom
            </button>
          )}
        </div>
      )}

      {/* Keyboard shortcut hints */}
      <div className={`flex items-center gap-4 px-3 py-1 border-t ${palette.border} ${palette.toolbarBg} text-[10px] ${palette.textDisabled}`}>
        <Shortcut keys="Ctrl+F" label="Search" palette={palette} />
        <Shortcut keys="Enter" label="Next match" palette={palette} />
        <Shortcut keys="Shift+Enter" label="Prev match" palette={palette} />
        <Shortcut keys="Esc" label="Close search" palette={palette} />
      </div>
    </div>
  )
}

function Shortcut({ keys, label, palette }: { keys: string; label: string; palette: LogPalette }) {
  return (
    <span className="flex items-center gap-1">
      <kbd className={`px-1 py-px rounded ${palette.elevatedBg} border ${palette.borderLight} font-mono`}>{keys}</kbd>
      <span>{label}</span>
    </span>
  )
}

interface LogLineProps {
  entry: LogEntry
  searchQuery: string
  searchIsRegex: boolean
  searchIsCaseSensitive: boolean
  showPodName: boolean
  showTimestamp: boolean
  tsFormat: TimestampFormat
  ansiEnabled: boolean
  isCurrentMatch: boolean
  wordWrap: boolean
  defaultExpanded: boolean
  onFilterValue?: (value: string) => void
  /** Optional lead element rendered at the start of the row (e.g. stack-trace toggle). */
  leadSlot?: ReactNode
  isDark: boolean
  palette: LogPalette
}

function LogLine({
  entry,
  searchQuery,
  searchIsRegex,
  searchIsCaseSensitive,
  showPodName,
  showTimestamp,
  tsFormat,
  ansiEnabled,
  isCurrentMatch,
  wordWrap,
  defaultExpanded,
  onFilterValue,
  leadSlot,
  isDark,
  palette,
}: LogLineProps) {
  const levelColor = getLogLevelColor(entry.level, isDark)
  const podTextColor = entry.podColorIndex !== undefined
    ? palette.podColors[entry.podColorIndex % palette.podColors.length].text
    : palette.textPrimary

  // Determine content rendering. Priority: search highlight > structured > ANSI/plain.
  let contentElement: React.ReactNode
  if (searchQuery) {
    const plain = stripAnsi(entry.content)
    const highlighted = highlightSearchMatches(plain, searchQuery, searchIsRegex, searchIsCaseSensitive)
    contentElement = (
      <span
        className={`${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'} ${levelColor}`}
        dangerouslySetInnerHTML={{ __html: highlighted }}
      />
    )
  } else if (entry.isJson || entry.isLogfmt) {
    contentElement = (
      <StructuredLogLine
        content={entry.content}
        level={entry.level}
        wordWrap={wordWrap}
        isLogfmt={entry.isLogfmt}
        defaultExpanded={defaultExpanded}
        onFilterValue={onFilterValue}
        isDark={isDark}
      />
    )
  } else if (ansiEnabled) {
    const html = ansiToHtml(entry.content)
    contentElement = (
      <span
        className={`${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'} ${levelColor}`}
        dangerouslySetInnerHTML={{ __html: html }}
      />
    )
  } else {
    contentElement = (
      <span className={`${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'} ${levelColor}`}>
        {stripAnsi(entry.content)}
      </span>
    )
  }

  const handleCopy = () => {
    const raw = stripAnsi(entry.content)
    navigator.clipboard.writeText(raw).catch(() => {})
  }

  return (
    <div className={`flex ${palette.hoverSurface} group leading-5 px-2 ${isCurrentMatch ? palette.currentMatchBg : ''}`}>
      {leadSlot}
      {showTimestamp && entry.timestamp && (
        <span
          className={`${palette.textTertiary} select-none pr-2 whitespace-nowrap`}
          title={entry.timestamp}
        >
          {formatLogTimestamp(entry.timestamp, tsFormat)}
        </span>
      )}
      {showPodName && entry.pod && (
        <span
          className={`${podTextColor} select-none pr-2 whitespace-nowrap min-w-[80px] max-w-[120px] truncate`}
          title={entry.pod}
        >
          [{entry.pod.split('-').slice(-2).join('-')}]
        </span>
      )}
      <span className="flex-1 min-w-0">{contentElement}</span>
      <button
        onClick={handleCopy}
        className={`opacity-0 group-hover:opacity-100 ml-1 p-0.5 rounded ${palette.textTertiary} ${palette.hoverText} shrink-0 transition-opacity`}
        title="Copy line"
      >
        <Copy className="w-3 h-3" />
      </button>
    </div>
  )
}

interface LogGroupItemProps {
  group: LogGroup
  searchQuery: string
  searchIsRegex: boolean
  searchIsCaseSensitive: boolean
  showPodName: boolean
  showTimestamp: boolean
  tsFormat: TimestampFormat
  ansiEnabled: boolean
  isCurrentMatch: boolean
  wordWrap: boolean
  defaultExpanded: boolean
  onFilterValue: (value: string) => void
  isStackExpanded: boolean
  onToggleStack: (id: number) => void
  isDark: boolean
  palette: LogPalette
}

function LogGroupItem(props: LogGroupItemProps) {
  const { group, isStackExpanded, onToggleStack, palette, ...rest } = props
  const hasStack = group.continuations.length > 0

  const stackToggle = hasStack ? (
    <button
      onClick={() => onToggleStack(group.head.id)}
      className={`mr-1 self-start p-0.5 rounded ${palette.textTertiary} ${palette.hoverText} ${palette.hoverSurface} shrink-0`}
      title={isStackExpanded ? 'Collapse stack trace' : `Expand ${group.continuations.length} stack frames`}
    >
      {isStackExpanded
        ? <ChevronDown className="w-3 h-3" />
        : <ChevronRight className="w-3 h-3" />}
    </button>
  ) : null

  return (
    <div>
      <LogLine
        entry={group.head}
        {...rest}
        palette={palette}
        leadSlot={stackToggle}
      />
      {hasStack && !isStackExpanded && (
        <button
          onClick={() => onToggleStack(group.head.id)}
          className={`block w-full text-left pl-6 pr-2 py-0 text-[10px] ${palette.textTertiary} ${palette.hoverText} ${palette.hoverSurface}`}
        >
          [+{group.continuations.length} stack {group.continuations.length === 1 ? 'line' : 'lines'}]
        </button>
      )}
      {hasStack && isStackExpanded && group.continuations.map(cont => (
        <div key={cont.id} className="pl-4">
          <LogLine entry={cont} {...rest} palette={palette} isCurrentMatch={false} />
        </div>
      ))}
    </div>
  )
}
