import { CaseSensitive, ChevronDown, ChevronUp, Search, X } from 'lucide-react'
import { clsx } from 'clsx'
import { Input } from '../ui/Input'
import { Tooltip } from '../ui/Tooltip'
import { isMac } from '../../utils/platform'
import { isTerminalFindShortcut } from './terminalKeys'
import { TERMINAL_SEARCH_LIMIT, type TerminalSearch, type TerminalSearchResults } from './useTerminalSearch'

const TIP_DELAY = 150

function findShortcutLabel(): string {
  return isMac() ? '⌘F' : 'Ctrl+Shift+F'
}

function countLabel(results: TerminalSearchResults): string {
  if (results.count === 0) return '0 results'
  if (results.count >= TERMINAL_SEARCH_LIMIT) return `${TERMINAL_SEARCH_LIMIT.toLocaleString()}+`
  if (results.index < 0) return results.count.toLocaleString()
  return `${results.index + 1} / ${results.count.toLocaleString()}`
}

/** Toolbar button that opens the search bar; sits beside Copy. */
export function TerminalSearchButton({ onClick, disabled }: { onClick: () => void; disabled?: boolean }) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      title={`Search output (${findShortcutLabel()})`}
      className="flex shrink-0 items-center gap-1 px-2 py-0.5 text-xs text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated rounded disabled:opacity-40 disabled:hover:bg-transparent disabled:cursor-default"
    >
      <Search className="w-3 h-3" />
      Search
    </button>
  )
}

/**
 * Floating find bar over the top-right of a terminal. It floats rather than
 * taking a row because shrinking the terminal would resize the shell's window
 * and reflow its whole buffer.
 *
 * Keys are handled on the whole bar, so they behave the same after clicking a
 * button. Only the keys handled here stop propagating: Escape must not also
 * restore a maximized dock, and the find shortcut must not open the browser's
 * find or a logs tab's search.
 */
export function TerminalSearchBar({ search }: { search: TerminalSearch }) {
  if (!search.isOpen) return null
  const mac = isMac()

  const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const native = e.nativeEvent
    // While an IME is composing, Escape and Enter belong to it. Escape still
    // must not reach the dock, which would restore itself from maximized.
    if (native.isComposing) {
      if (e.key === 'Escape') e.stopPropagation()
      return
    }
    let handled = true
    if (e.key === 'Escape') {
      search.close()
    } else if (isTerminalFindShortcut(native, mac)) {
      search.inputRef.current?.focus()
      search.inputRef.current?.select()
    } else if (e.key === 'F3' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      if (e.shiftKey) search.findNewer()
      else search.findOlder()
    } else if (mac && e.metaKey && !e.ctrlKey && !e.altKey && e.key.toLowerCase() === 'g') {
      if (e.shiftKey) search.findNewer()
      else search.findOlder()
    } else if (e.key === 'Enter' && e.target === search.inputRef.current) {
      if (e.shiftKey) search.findNewer()
      else search.findOlder()
    } else {
      handled = false
    }
    if (handled) {
      e.preventDefault()
      e.stopPropagation()
    }
  }

  const { results, query } = search
  const hasMatches = !!results && results.count > 0
  const iconButton = 'p-1 rounded text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover disabled:opacity-30 disabled:hover:bg-transparent'

  return (
    <div
      role="search"
      onKeyDown={handleKeyDown}
      className="absolute top-9 right-3 z-10 w-[min(26rem,calc(100%-1.5rem))] rounded-md border border-theme-border bg-theme-surface px-2 py-1 shadow-theme-md"
    >
      <div className="flex items-center gap-1.5">
        <Search className="w-3.5 h-3.5 shrink-0 text-theme-text-tertiary" />
        <Input
          ref={search.inputRef}
          value={query}
          onChange={(e) => search.setQuery(e.target.value)}
          placeholder="Search output"
          aria-label="Search terminal output"
          className="min-w-0 flex-1 bg-transparent text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none"
        />
        {query && results && (
          <span
            className={clsx(
              'shrink-0 whitespace-nowrap text-xs',
              search.isStale ? 'text-theme-text-disabled' : 'text-theme-text-tertiary',
            )}
          >
            {countLabel(results)}
          </span>
        )}
        <Tooltip content="Match case" delay={TIP_DELAY} position="bottom">
          <button
            onClick={search.toggleCaseSensitive}
            aria-pressed={search.caseSensitive}
            aria-label="Match case"
            className={clsx(
              'p-1 rounded',
              search.caseSensitive ? 'bg-theme-elevated text-theme-text-primary' : 'text-theme-text-tertiary hover:text-theme-text-primary',
            )}
          >
            <CaseSensitive className="w-3.5 h-3.5" />
          </button>
        </Tooltip>
        <Tooltip content="Older match (Enter)" delay={TIP_DELAY} position="bottom">
          <button onClick={search.findOlder} disabled={!hasMatches} aria-label="Older match" className={iconButton}>
            <ChevronUp className="w-3.5 h-3.5" />
          </button>
        </Tooltip>
        <Tooltip content="Newer match (Shift+Enter)" delay={TIP_DELAY} position="bottom">
          <button onClick={search.findNewer} disabled={!hasMatches} aria-label="Newer match" className={iconButton}>
            <ChevronDown className="w-3.5 h-3.5" />
          </button>
        </Tooltip>
        <Tooltip content="Close (Esc)" delay={TIP_DELAY} position="bottom">
          <button onClick={search.close} aria-label="Close search" className={iconButton}>
            <X className="w-3.5 h-3.5" />
          </button>
        </Tooltip>
      </div>
      {query && results?.count === 0 && results.historyFull && (
        <div className="px-5 pb-0.5 text-xs text-theme-text-tertiary">Older output is no longer kept</div>
      )}
    </div>
  )
}
