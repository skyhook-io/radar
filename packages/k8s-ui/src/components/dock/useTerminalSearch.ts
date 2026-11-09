import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { IDisposable, Terminal as XTerm } from '@xterm/xterm'
import { SearchAddon, type ISearchOptions } from '@xterm/addon-search'
import { useDockContentVisible } from './DockContext'

/** Matches past this many are counted as "limit or more" and not highlighted. */
export const TERMINAL_SEARCH_LIMIT = 1000

// Quiet time after output before the search is re-run. Shorter than the
// addon's own 200 ms pass, which reuses cached line text that can be stale;
// replacing the addon first cancels that pass.
const CATCH_UP_MS = 150

// xterm draws decorations from these and only accepts #RRGGBB.
const DECORATIONS: NonNullable<ISearchOptions['decorations']> = {
  matchBackground: '#713f12',
  matchOverviewRuler: '#713f12',
  activeMatchBackground: '#b45309',
  activeMatchBorder: '#fde047',
  activeMatchColorOverviewRuler: '#fde047',
}

function focusQuery(input: HTMLInputElement | null) {
  input?.focus()
  input?.select()
}

function searchOptions(caseSensitive: boolean): ISearchOptions {
  return { caseSensitive, decorations: DECORATIONS }
}

export interface TerminalSearchResults {
  /** Position of the selected match, or -1 when it is unknown. */
  index: number
  /** Number of matches, capped at TERMINAL_SEARCH_LIMIT. */
  count: number
  /** The terminal is holding as many lines as it keeps, so older output has been dropped. */
  historyFull: boolean
}

export interface TerminalSearch {
  isOpen: boolean
  query: string
  caseSensitive: boolean
  results: TerminalSearchResults | null
  /** Output arrived after `results` was computed; the count may be behind. */
  isStale: boolean
  inputRef: React.RefObject<HTMLInputElement | null>
  open: () => void
  close: () => void
  setQuery: (query: string) => void
  toggleCaseSensitive: () => void
  findOlder: () => void
  findNewer: () => void
  /** Connects a freshly opened terminal. The returned disposer runs before that terminal is disposed. */
  attach: (xterm: XTerm) => () => void
  /** Focus the search input while it is open, otherwise the terminal. */
  restoreFocus: () => void
}

/**
 * Find-in-terminal state for one terminal tab.
 *
 * A search addon exists only while a query is being searched on screen. Every
 * full scan (new query, option change, reopen, return to the tab, and the
 * catch-up after output goes quiet) gets a fresh addon, and closing or hiding
 * disposes it. A terminal nobody searches keeps today's output path with no
 * extra listener per write. A fresh addon also sidesteps three addon quirks:
 * its option-change check compares options it has already overwritten, its
 * cached line text survives in-place rewrites that end with the cursor where
 * it started, and clearing its decorations leaves a pending catch-up search
 * that later clears the user's selection.
 *
 * Results only count as fresh when they come from a full scan. Stepping
 * between matches fires the same results event without re-scanning, so those
 * events leave a stale count stale.
 */
export function useTerminalSearch(isActive: boolean): TerminalSearch {
  const isVisible = useDockContentVisible() && isActive
  const [isOpen, setIsOpen] = useState(false)
  const [query, setQueryState] = useState('')
  const [caseSensitive, setCaseSensitive] = useState(false)
  const [results, setResults] = useState<TerminalSearchResults | null>(null)
  const [isStale, setIsStale] = useState(false)
  const inputRef = useRef<HTMLInputElement | null>(null)

  const xtermRef = useRef<XTerm | null>(null)
  const addonRef = useRef<{ addon: SearchAddon; resultsListener: IDisposable } | null>(null)
  const writeListenerRef = useRef<IDisposable | null>(null)
  const catchUpTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const isVisibleRef = useRef(isVisible)
  const steppingRef = useRef(false)
  const staleRef = useRef(false)
  const isOpenRef = useRef(false)
  const queryRef = useRef('')
  const caseRef = useRef(false)

  const markStale = useCallback((stale: boolean) => {
    if (staleRef.current === stale) return
    staleRef.current = stale
    setIsStale(stale)
  }, [])

  const disposeAddon = useCallback(() => {
    addonRef.current?.resultsListener.dispose()
    addonRef.current?.addon.dispose()
    addonRef.current = null
  }, [])

  const freshAddon = useCallback((xterm: XTerm): SearchAddon => {
    disposeAddon()
    const addon = new SearchAddon({ highlightLimit: TERMINAL_SEARCH_LIMIT })
    xterm.loadAddon(addon)
    const resultsListener = addon.onDidChangeResults(({ resultIndex, resultCount }) => {
      const buffer = xterm.buffer.active
      const kept = (xterm.options.scrollback ?? 0) + xterm.rows
      setResults({ index: resultIndex, count: resultCount, historyFull: buffer.type === 'normal' && buffer.length >= kept })
      if (!steppingRef.current) markStale(false)
    })
    addonRef.current = { addon, resultsListener }
    return addon
  }, [disposeAddon, markStale])

  // A new or changed query starts from the newest output: the addon searches
  // backwards from the current selection.
  const searchFromBottom = useCallback((term: string) => {
    const xterm = xtermRef.current
    if (!xterm) return
    xterm.clearSelection()
    if (!term) {
      disposeAddon()
      setResults(null)
      markStale(false)
      return
    }
    freshAddon(xterm).findPrevious(term, searchOptions(caseRef.current))
  }, [disposeAddon, freshAddon, markStale])

  const step = useCallback((older: boolean) => {
    const addon = addonRef.current?.addon
    const term = queryRef.current
    if (!addon || !term) return
    steppingRef.current = true
    try {
      if (older) addon.findPrevious(term, searchOptions(caseRef.current))
      else addon.findNext(term, searchOptions(caseRef.current))
    } finally {
      steppingRef.current = false
    }
  }, [])

  // Output changed the buffer: scan again where the user is. The search keeps
  // the current match when it still matches, and the view stays put.
  const catchUp = useCallback(() => {
    catchUpTimerRef.current = null
    const xterm = xtermRef.current
    const term = queryRef.current
    if (!xterm || !term || !isOpenRef.current || !isVisibleRef.current) return
    const viewportY = xterm.buffer.active.viewportY
    freshAddon(xterm).findPrevious(term, { ...searchOptions(caseRef.current), incremental: true })
    if (xterm.buffer.active.viewportY !== viewportY) xterm.scrollToLine(viewportY)
  }, [freshAddon])

  const stopWatchingWrites = useCallback(() => {
    writeListenerRef.current?.dispose()
    writeListenerRef.current = null
    if (catchUpTimerRef.current) clearTimeout(catchUpTimerRef.current)
    catchUpTimerRef.current = null
  }, [])

  const clearHighlights = useCallback(() => {
    // The addon marks the current match by selecting it. Without a search
    // the selection is the user's own, and Copy still needs it.
    if (addonRef.current) xtermRef.current?.clearSelection()
    disposeAddon()
    setResults(null)
    markStale(false)
  }, [disposeAddon, markStale])

  const open = useCallback(() => {
    if (!xtermRef.current) return
    const xterm = xtermRef.current
    const reopening = !isOpenRef.current
    isOpenRef.current = true
    setIsOpen(true)
    if (!writeListenerRef.current) {
      writeListenerRef.current = xterm.onWriteParsed(() => {
        if (!queryRef.current) return
        markStale(true)
        if (catchUpTimerRef.current) clearTimeout(catchUpTimerRef.current)
        catchUpTimerRef.current = setTimeout(catchUp, CATCH_UP_MS)
      })
    }
    if (reopening && queryRef.current) searchFromBottom(queryRef.current)
    // Already open: the shortcut jumps back to the query. A fresh bar is
    // focused by the layout effect below once its input exists.
    if (!reopening) focusQuery(inputRef.current)
  }, [searchFromBottom, markStale, catchUp])

  useLayoutEffect(() => {
    if (isOpen) focusQuery(inputRef.current)
  }, [isOpen])

  const close = useCallback(() => {
    if (!isOpenRef.current) return
    isOpenRef.current = false
    setIsOpen(false)
    stopWatchingWrites()
    clearHighlights()
    xtermRef.current?.focus()
  }, [clearHighlights, stopWatchingWrites])

  const setQuery = useCallback((next: string) => {
    queryRef.current = next
    setQueryState(next)
    searchFromBottom(next)
  }, [searchFromBottom])

  const toggleCaseSensitive = useCallback(() => {
    caseRef.current = !caseRef.current
    setCaseSensitive(caseRef.current)
    searchFromBottom(queryRef.current)
  }, [searchFromBottom])

  const findOlder = useCallback(() => step(true), [step])
  const findNewer = useCallback(() => step(false), [step])

  const attach = useCallback((xterm: XTerm) => {
    xtermRef.current = xterm
    return () => {
      disposeAddon()
      stopWatchingWrites()
      if (xtermRef.current === xterm) xtermRef.current = null
      // A new session starts with empty output, so the old query has nothing to find.
      isOpenRef.current = false
      setIsOpen(false)
      setResults(null)
      markStale(false)
    }
  }, [disposeAddon, markStale, stopWatchingWrites])

  const restoreFocus = useCallback(() => {
    if (isOpenRef.current && inputRef.current) inputRef.current.focus()
    else xtermRef.current?.focus()
  }, [])

  // Hidden tabs and a collapsed dock stay mounted and keep receiving output.
  // Drop their highlights so nobody pays for searches nobody can see, and
  // search again when the terminal is back on screen.
  useEffect(() => {
    if (isVisibleRef.current === isVisible) return
    isVisibleRef.current = isVisible
    if (!isOpenRef.current) return
    if (isVisible) searchFromBottom(queryRef.current)
    else clearHighlights()
  }, [isVisible, searchFromBottom, clearHighlights])

  return useMemo(() => ({
    isOpen, query, caseSensitive, results, isStale, inputRef,
    open, close, setQuery, toggleCaseSensitive, findOlder, findNewer, attach, restoreFocus,
  }), [isOpen, query, caseSensitive, results, isStale, open, close, setQuery, toggleCaseSensitive, findOlder, findNewer, attach, restoreFocus])
}
