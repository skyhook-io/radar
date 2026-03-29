import { useRef, useCallback, useState, useMemo, useEffect, type ReactNode } from 'react'
import { Virtuoso, type VirtuosoHandle } from 'react-virtuoso'
import { Play, Square, Download, Search, X, Terminal, RotateCcw, ChevronUp, ChevronDown, CaseSensitive, Regex, WrapText, Clock, Copy, Trash2, Filter, Braces, Sun, Moon } from 'lucide-react'
import type { LogEntry, LogLevel } from './useLogBuffer'
import { useLogSearch } from './useLogSearch'
import { StructuredLogLine } from './StructuredLogLine'
import { Tooltip } from '../ui/Tooltip'
import {
  formatLogTimestamp,
  getLevelColor,
  highlightSearchMatches,
  stripAnsi,
  ansiToHtml,
} from '../../utils/log-format'

export type DownloadFormat = 'txt' | 'json' | 'csv'

interface LogCoreProps {
  entries: LogEntry[]
  isLoading: boolean
  isStreaming: boolean
  onStartStream?: () => void
  onStopStream: () => void
  onRefresh: () => void
  onDownload: (format: DownloadFormat) => void
  onClear?: () => void
  toolbarExtra?: ReactNode
  showPodName?: boolean
  emptyMessage?: string
  errorMessage?: string | null
  /** Force dark mode on the logs container (default: true). Logs are typically dark-themed. */
  forceDark?: boolean
}

const LEVEL_OPTIONS: { level: LogLevel; label: string; color: string; activeColor: string }[] = [
  { level: 'error', label: 'ERR', color: 'text-red-400', activeColor: 'bg-red-500/30 dark:bg-red-500/20 text-red-600 dark:text-red-400 border-red-500/60 dark:border-red-500/40' },
  { level: 'warn', label: 'WARN', color: 'text-yellow-400', activeColor: 'bg-yellow-400/30 dark:bg-yellow-500/20 text-yellow-700 dark:text-yellow-400 border-yellow-500/60 dark:border-yellow-500/40' },
  { level: 'info', label: 'INFO', color: 'text-blue-400', activeColor: 'bg-blue-500/25 dark:bg-blue-500/20 text-blue-700 dark:text-blue-400 border-blue-500/60 dark:border-blue-500/40' },
  { level: 'debug', label: 'DBG', color: 'text-theme-text-secondary', activeColor: 'bg-theme-surface text-theme-text-secondary border-theme-border-light' },
]

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
  forceDark = true,
}: LogCoreProps) {
  const virtuosoRef = useRef<VirtuosoHandle>(null)
  const [atBottom, setAtBottom] = useState(true)
  const [isDark, setIsDark] = useState(() => {
    try { const v = localStorage.getItem('radar-logs-dark'); return v !== null ? v !== 'false' : forceDark } catch { return forceDark }
  })
  const [wordWrap, setWordWrap] = useState(() => {
    try { return localStorage.getItem('radar-logs-wrap') !== 'false' } catch { return true }
  })
  const [showTimestamps, setShowTimestamps] = useState(() => {
    try { return localStorage.getItem('radar-logs-timestamps') !== 'false' } catch { return true }
  })
  const [enabledLevels, setEnabledLevels] = useState<Set<LogLevel>>(
    new Set(['error', 'warn', 'info', 'debug'])
  )
  const [showDownloadMenu, setShowDownloadMenu] = useState(false)
  const [expandAllStructured, setExpandAllStructured] = useState(false)

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

  const scrollToBottom = useCallback(() => {
    virtuosoRef.current?.scrollToIndex({
      index: displayEntries.length - 1,
      align: 'end',
      behavior: 'smooth',
    })
  }, [displayEntries.length])

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

  return (
    <div className={`flex flex-col h-full bg-theme-base${isDark ? ' dark' : ''}`} style={{ colorScheme: isDark ? 'dark' : 'light', fontFamily: "'SF Mono', 'Cascadia Code', 'Fira Code', Menlo, Consolas, 'DejaVu Sans Mono', monospace" }}>
      {/* Toolbar */}
      <div className="flex items-center gap-2 px-3 py-2 border-b border-theme-border bg-theme-surface">
        {toolbarExtra}

        {/* Stream / Stop toggle — only shown when streaming is supported */}
        {onStartStream && (
          <Tooltip content={isStreaming ? 'Stop streaming' : 'Start streaming'} delay={TIP_DELAY} position="bottom">
            <button
              onClick={isStreaming ? onStopStream : onStartStream}
              className={`flex items-center gap-1.5 px-2 py-1.5 text-xs rounded transition-colors ${
                isStreaming
                  ? 'bg-green-600 text-white hover:bg-green-700'
                  : 'bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover'
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
            className="flex items-center gap-1.5 px-2 py-1.5 text-xs rounded bg-theme-elevated text-theme-text-secondary hover:bg-theme-hover disabled:opacity-50 disabled:cursor-not-allowed"
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
                      ? opt.activeColor
                      : 'border-transparent text-theme-text-disabled hover:text-theme-text-tertiary'
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
                expandAllStructured ? 'btn-brand-toggle' : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
              }`}
            >
              <Braces className="w-4 h-4" />
            </button>
          </Tooltip>
        )}

        {/* Timestamp toggle */}
        <Tooltip content={showTimestamps ? 'Hide timestamps' : 'Show timestamps'} delay={TIP_DELAY} position="bottom">
          <button
            onClick={toggleTimestamps}
            className={`p-1.5 rounded transition-colors ${
              showTimestamps ? 'btn-brand-toggle' : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
            }`}
          >
            <Clock className="w-4 h-4" />
          </button>
        </Tooltip>

        {/* Wrap toggle */}
        <Tooltip content={wordWrap ? 'Disable word wrap' : 'Enable word wrap'} delay={TIP_DELAY} position="bottom">
          <button
            onClick={toggleWrap}
            className={`p-1.5 rounded transition-colors ${
              wordWrap ? 'btn-brand-toggle' : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
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
              search.isOpen ? 'btn-brand-toggle' : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
            }`}
          >
            <Search className="w-4 h-4" />
          </button>
        </Tooltip>

        {/* Download */}
        <div className="relative flex items-center" ref={downloadMenuRef}>
          <Tooltip content="Download logs" delay={TIP_DELAY} position="bottom">
            <button
              onClick={() => setShowDownloadMenu(prev => !prev)}
              className="p-1.5 rounded text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated"
            >
              <Download className="w-4 h-4" />
            </button>
          </Tooltip>
          {showDownloadMenu && (
            <div className="absolute top-full right-0 mt-1 w-32 bg-theme-elevated border border-theme-border rounded-lg shadow-lg z-50">
              {(['txt', 'json', 'csv'] as DownloadFormat[]).map(fmt => (
                <button
                  key={fmt}
                  onClick={() => { onDownload(fmt); setShowDownloadMenu(false) }}
                  className="w-full text-left px-3 py-2 text-xs text-theme-text-primary hover:bg-theme-hover first:rounded-t-lg last:rounded-b-lg"
                >
                  {fmt.toUpperCase()}
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Dark/Light toggle */}
        <Tooltip content={isDark ? 'Light mode' : 'Dark mode'} delay={TIP_DELAY} position="bottom">
          <button
            onClick={() => {
              const next = !isDark
              setIsDark(next)
              try { localStorage.setItem('radar-logs-dark', String(next)) } catch {}
            }}
            className="p-1.5 rounded text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated"
          >
            {isDark ? <Sun className="w-4 h-4" /> : <Moon className="w-4 h-4" />}
          </button>
        </Tooltip>

        {/* Clear */}
        {onClear && (
          <Tooltip content="Clear logs" delay={TIP_DELAY} position="bottom">
            <button
              onClick={onClear}
              className="p-1.5 rounded text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated"
            >
              <Trash2 className="w-4 h-4" />
            </button>
          </Tooltip>
        )}
      </div>

      {/* Search bar */}
      {search.isOpen && (
        <div className="flex items-center gap-2 px-3 py-2 border-b border-theme-border bg-theme-surface/50">
          <Search className="w-4 h-4 text-theme-text-secondary shrink-0" />
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
            className="flex-1 bg-transparent text-theme-text-primary text-sm placeholder-theme-text-disabled focus:outline-none min-w-0"
            autoFocus
          />

          {/* Regex toggle */}
          <Tooltip content="Regex" delay={TIP_DELAY} position="bottom">
            <button
              onClick={search.toggleRegex}
              className={`p-1 rounded transition-colors ${
                search.isRegex ? 'btn-brand-toggle' : 'text-theme-text-tertiary hover:text-theme-text-secondary'
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
                search.isCaseSensitive ? 'btn-brand-toggle' : 'text-theme-text-tertiary hover:text-theme-text-secondary'
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
                search.isFilterMode ? 'btn-brand-toggle' : 'text-theme-text-tertiary hover:text-theme-text-secondary'
              }`}
            >
              <Filter className="w-3.5 h-3.5" />
            </button>
          </Tooltip>

          {search.query && (
            <>
              <span className={`text-xs whitespace-nowrap ${search.regexError ? 'text-red-400' : 'text-theme-text-tertiary'}`}>
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
                  className="p-1 rounded text-theme-text-secondary hover:text-theme-text-primary disabled:opacity-30"
                >
                  <ChevronUp className="w-3.5 h-3.5" />
                </button>
              </Tooltip>
              <Tooltip content="Next (Enter)" delay={TIP_DELAY} position="bottom">
                <button
                  onClick={search.goToNext}
                  disabled={search.matchCount === 0}
                  className="p-1 rounded text-theme-text-secondary hover:text-theme-text-primary disabled:opacity-30"
                >
                  <ChevronDown className="w-3.5 h-3.5" />
                </button>
              </Tooltip>

              <button
                onClick={() => search.setQuery('')}
                className="p-1 rounded text-theme-text-secondary hover:text-theme-text-primary"
              >
                <X className="w-3 h-3" />
              </button>
            </>
          )}
        </div>
      )}

      {/* Log content */}
      {isLoading && entries.length === 0 ? (
        <div className="flex-1 flex items-center justify-center text-theme-text-tertiary">
          <div className="flex items-center gap-2">
            <RotateCcw className="w-4 h-4 animate-spin" />
            <span>Loading logs...</span>
          </div>
        </div>
      ) : errorMessage ? (
        <div className="flex-1 flex flex-col items-center justify-center text-red-400 gap-2">
          <Terminal className="w-8 h-8" />
          <span>{errorMessage}</span>
        </div>
      ) : displayEntries.length === 0 ? (
        <div className="flex-1 flex flex-col items-center justify-center text-theme-text-tertiary gap-2">
          <Terminal className="w-8 h-8" />
          <span>{emptyMessage}</span>
        </div>
      ) : (
        <div className="flex-1 relative">
          <Virtuoso
            ref={virtuosoRef}
            data={displayEntries}
            followOutput={handleFollowOutput}
            initialTopMostItemIndex={displayEntries.length - 1}
            atBottomStateChange={handleAtBottomStateChange}
            atBottomThreshold={50}
            increaseViewportBy={200}
            itemContent={(_index, entry) => (
              <LogLine
                entry={entry}
                searchQuery={search.query}
                searchIsRegex={search.isRegex}
                searchIsCaseSensitive={search.isCaseSensitive}
                showPodName={showPodName}
                showTimestamp={showTimestamps}
                isCurrentMatch={entry.id === currentHighlightId}
                wordWrap={wordWrap}
                defaultExpanded={expandAllStructured}
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
      <div className="flex items-center gap-4 px-3 py-1 border-t border-theme-border bg-theme-surface text-[10px] text-theme-text-disabled">
        <Shortcut keys="Ctrl+F" label="Search" />
        <Shortcut keys="Enter" label="Next match" />
        <Shortcut keys="Shift+Enter" label="Prev match" />
        <Shortcut keys="Esc" label="Close search" />
      </div>
    </div>
  )
}

function Shortcut({ keys, label }: { keys: string; label: string }) {
  return (
    <span className="flex items-center gap-1">
      <kbd className="px-1 py-px rounded bg-theme-elevated border border-theme-border-light font-mono">{keys}</kbd>
      <span>{label}</span>
    </span>
  )
}

function LogLine({
  entry,
  searchQuery,
  searchIsRegex,
  searchIsCaseSensitive,
  showPodName,
  showTimestamp,
  isCurrentMatch,
  wordWrap,
  defaultExpanded,
}: {
  entry: LogEntry
  searchQuery: string
  searchIsRegex: boolean
  searchIsCaseSensitive: boolean
  showPodName: boolean
  showTimestamp: boolean
  isCurrentMatch: boolean
  wordWrap: boolean
  defaultExpanded: boolean
}) {
  const levelColor = getLevelColor(entry.level)

  // Determine content rendering
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
      />
    )
  } else {
    const html = ansiToHtml(entry.content)
    contentElement = (
      <span
        className={`${wordWrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'} ${levelColor}`}
        dangerouslySetInnerHTML={{ __html: html }}
      />
    )
  }

  const handleCopy = () => {
    const raw = stripAnsi(entry.content)
    navigator.clipboard.writeText(raw).catch(() => {})
  }

  return (
    <div className={`flex hover:bg-theme-surface/50 group leading-5 px-2 ${isCurrentMatch ? 'bg-yellow-500/10' : ''}`}>
      {showTimestamp && entry.timestamp && (
        <span className="text-theme-text-tertiary select-none pr-2 whitespace-nowrap">
          {formatLogTimestamp(entry.timestamp)}
        </span>
      )}
      {showPodName && entry.pod && (
        <span
          className={`${entry.podColor || 'text-theme-text-primary'} select-none pr-2 whitespace-nowrap min-w-[80px] max-w-[120px] truncate`}
          title={entry.pod}
        >
          [{entry.pod.split('-').slice(-2).join('-')}]
        </span>
      )}
      <span className="flex-1 min-w-0">{contentElement}</span>
      <button
        onClick={handleCopy}
        className="opacity-0 group-hover:opacity-100 ml-1 p-0.5 rounded text-theme-text-tertiary hover:text-theme-text-secondary shrink-0 transition-opacity"
        title="Copy line"
      >
        <Copy className="w-3 h-3" />
      </button>
    </div>
  )
}
