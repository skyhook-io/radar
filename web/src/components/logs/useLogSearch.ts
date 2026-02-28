import { useState, useMemo, useCallback, useEffect, useRef } from 'react'
import type { VirtuosoHandle } from 'react-virtuoso'
import type { LogEntry } from './useLogBuffer'
import { stripAnsi, escapeRegExp } from '../../utils/log-format'

interface UseLogSearchReturn {
  query: string
  setQuery: (q: string) => void
  isRegex: boolean
  toggleRegex: () => void
  isCaseSensitive: boolean
  toggleCaseSensitive: () => void
  isFilterMode: boolean
  toggleFilterMode: () => void
  matchCount: number
  currentMatch: number
  /** Indices into the entries array that match */
  matchIndices: number[]
  /** When filter mode is on, only matching entries */
  filteredEntries: LogEntry[]
  /** Error message when regex is invalid (null when valid) */
  regexError: string | null
  goToNext: () => void
  goToPrev: () => void
  isOpen: boolean
  open: () => void
  close: () => void
}

export function useLogSearch(
  entries: LogEntry[],
  virtuosoRef: React.RefObject<VirtuosoHandle | null>,
): UseLogSearchReturn {
  const [query, setQuery] = useState('')
  const [isRegex, setIsRegex] = useState(false)
  const [isCaseSensitive, setIsCaseSensitive] = useState(false)
  const [isFilterMode, setIsFilterMode] = useState(false)
  const [currentMatch, setCurrentMatch] = useState(0)
  const [isOpen, setIsOpen] = useState(false)

  const { matchIndices, regexError } = useMemo(() => {
    if (!query) {
      return { matchIndices: [] as number[], regexError: null }
    }

    try {
      let pattern: RegExp
      if (isRegex) {
        pattern = new RegExp(query, isCaseSensitive ? 'g' : 'gi')
      } else {
        pattern = new RegExp(escapeRegExp(query), isCaseSensitive ? 'g' : 'gi')
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
  }, [entries, query, isRegex, isCaseSensitive])

  // Filtered entries for filter mode
  const filteredEntries = useMemo(() => {
    if (!isFilterMode || !query) return entries
    const matchSet = new Set(matchIndices)
    return entries.filter((_, i) => matchSet.has(i))
  }, [entries, isFilterMode, query, matchIndices])

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
    if (isFilterMode) {
      // In filter mode, match index maps directly to filtered list index
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
  }, [matchIndices, isFilterMode, virtuosoRef])

  const goToNext = useCallback(() => {
    if (matchIndices.length === 0) return
    const next = (currentMatch + 1) % matchIndices.length
    setCurrentMatch(next)
    scrollToMatch(next)
  }, [currentMatch, matchIndices.length, scrollToMatch])

  const goToPrev = useCallback(() => {
    if (matchIndices.length === 0) return
    const prev = (currentMatch - 1 + matchIndices.length) % matchIndices.length
    setCurrentMatch(prev)
    scrollToMatch(prev)
  }, [currentMatch, matchIndices.length, scrollToMatch])

  const toggleRegex = useCallback(() => setIsRegex(p => !p), [])
  const toggleCaseSensitive = useCallback(() => setIsCaseSensitive(p => !p), [])
  const toggleFilterMode = useCallback(() => setIsFilterMode(p => !p), [])

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
    isCaseSensitive,
    toggleCaseSensitive,
    isFilterMode,
    toggleFilterMode,
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
