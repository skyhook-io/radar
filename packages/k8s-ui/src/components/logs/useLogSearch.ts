import { useState, useMemo, useCallback, useDeferredValue, useEffect, useRef } from 'react'
import type { VirtuosoHandle } from 'react-virtuoso'
import type { LogEntry } from './useLogBuffer'
import { stripAnsi, escapeRegExp } from '../../utils/log-format'

export type LogSearchMode = 'highlight' | 'only' | 'hide'

interface UseLogSearchReturn {
  query: string
  setQuery: (q: string) => void
  isRegex: boolean
  toggleRegex: () => void
  setIsRegex: (v: boolean) => void
  isCaseSensitive: boolean
  toggleCaseSensitive: () => void
  /** highlight: show every line, mark matches · only: show matching lines · hide: drop matching lines */
  mode: LogSearchMode
  setMode: (mode: LogSearchMode) => void
  /** Whether the query currently narrows the visible lines (only/hide with a non-empty, valid query) */
  isFiltering: boolean
  matchCount: number
  currentMatch: number
  /** Indices into the entries array that match */
  matchIndices: number[]
  /** The lines left after applying the mode: matching lines for `only`, non-matching for `hide` */
  filteredEntries: LogEntry[]
  /** Error message when regex is invalid (null when valid) */
  regexError: string | null
  goToNext: () => void
  goToPrev: () => void
  isOpen: boolean
  open: () => void
  close: () => void
}

/** The lines a search mode leaves visible, given the indices of matching lines. */
export function applySearchMode<T>(entries: readonly T[], matchIndices: readonly number[], mode: LogSearchMode): T[] {
  if (mode === 'highlight') return [...entries]
  const matchSet = new Set(matchIndices)
  const keepMatches = mode === 'only'
  return entries.filter((_, i) => matchSet.has(i) === keepMatches)
}

export function useLogSearch(
  entries: LogEntry[],
  virtuosoRef: React.RefObject<VirtuosoHandle | null>,
): UseLogSearchReturn {
  const [query, setQuery] = useState('')
  const [isRegex, setIsRegex] = useState(false)
  const [isCaseSensitive, setIsCaseSensitive] = useState(false)
  const [mode, setMode] = useState<LogSearchMode>('highlight')
  const [currentMatch, setCurrentMatch] = useState(0)
  const [isOpen, setIsOpen] = useState(false)

  // The scan strips ANSI and regex-tests every buffered line — on a multi-MB
  // buffer that's too slow to run synchronously per keystroke. Deferring lets
  // the input echo immediately while the match set catches up.
  const deferredQuery = useDeferredValue(query)

  const { matchIndices, regexError } = useMemo(() => {
    if (!deferredQuery) {
      return { matchIndices: [] as number[], regexError: null }
    }

    try {
      let pattern: RegExp
      if (isRegex) {
        pattern = new RegExp(deferredQuery, isCaseSensitive ? 'g' : 'gi')
      } else {
        pattern = new RegExp(escapeRegExp(deferredQuery), isCaseSensitive ? 'g' : 'gi')
      }

      const indices: number[] = []
      for (let i = 0; i < entries.length; i++) {
        const plain = stripAnsi(entries[i].content)
        if (pattern.test(plain)) {
          indices.push(i)
        }
        pattern.lastIndex = 0
      }
      return { matchIndices: indices, regexError: null }
    } catch (e) {
      return { matchIndices: [] as number[], regexError: e instanceof Error ? e.message : 'Invalid regex' }
    }
  }, [entries, deferredQuery, isRegex, isCaseSensitive])

  // Filtered entries for filter mode
  // Gate on the live query too, so clearing or closing search unfilters immediately
  // rather than after the deferred value catches up.
  const isFiltering = mode !== 'highlight' && !!query && !!deferredQuery && !regexError
  const filteredEntries = useMemo(
    () => (isFiltering ? applySearchMode(entries, matchIndices, mode) : entries),
    [entries, isFiltering, mode, matchIndices],
  )

  // Reset current match when search criteria change (but not when new entries arrive during streaming)
  const prevCriteria = useRef({ query, isRegex, isCaseSensitive })
  useEffect(() => {
    if (
      prevCriteria.current.query !== query ||
      prevCriteria.current.isRegex !== isRegex ||
      prevCriteria.current.isCaseSensitive !== isCaseSensitive
    ) {
      setCurrentMatch(0)
      prevCriteria.current = { query, isRegex, isCaseSensitive }
    }
  }, [query, isRegex, isCaseSensitive])

  const scrollToMatch = useCallback((matchIdx: number) => {
    if (matchIdx < 0 || matchIdx >= matchIndices.length) return
    if (mode === 'hide') return
    if (mode === 'only') {
      // Only matching lines are listed, so the match index is the list index
      virtuosoRef.current?.scrollToIndex({
        index: matchIdx,
        align: 'center',
        behavior: 'smooth',
      })
    } else {
      const entryIndex = matchIndices[matchIdx]
      virtuosoRef.current?.scrollToIndex({
        index: entryIndex,
        align: 'center',
        behavior: 'smooth',
      })
    }
  }, [matchIndices, mode, virtuosoRef])

  const goToNext = useCallback(() => {
    if (matchIndices.length === 0 || mode === 'hide') return
    const next = (currentMatch + 1) % matchIndices.length
    setCurrentMatch(next)
    scrollToMatch(next)
  }, [currentMatch, matchIndices.length, mode, scrollToMatch])

  const goToPrev = useCallback(() => {
    if (matchIndices.length === 0 || mode === 'hide') return
    const prev = (currentMatch - 1 + matchIndices.length) % matchIndices.length
    setCurrentMatch(prev)
    scrollToMatch(prev)
  }, [currentMatch, matchIndices.length, mode, scrollToMatch])

  const toggleRegex = useCallback(() => setIsRegex(p => !p), [])
  const toggleCaseSensitive = useCallback(() => setIsCaseSensitive(p => !p), [])

  const open = useCallback(() => setIsOpen(true), [])
  const close = useCallback(() => {
    setIsOpen(false)
    setQuery('')
  }, [])

  return {
    query,
    setQuery,
    isRegex,
    toggleRegex,
    setIsRegex,
    isCaseSensitive,
    toggleCaseSensitive,
    mode,
    setMode,
    isFiltering,
    matchCount: matchIndices.length,
    currentMatch,
    matchIndices,
    filteredEntries,
    regexError,
    goToNext,
    goToPrev,
    isOpen,
    open,
    close,
  }
}
