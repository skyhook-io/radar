import { useState, useRef, useCallback } from 'react'
import { isLogfmt } from '../../utils/log-format'
import { detectLevel, type LevelSource, type LogLevel } from '../../utils/log-level'

export type { LevelSource, LogLevel }
export { detectLogLevel } from '../../utils/log-level'

export interface LogEntry {
  sourceLabel?: string
  id: number
  timestamp: string
  content: string
  container: string
  pod?: string
  /**
   * Index into the current palette's `podColors` array. Stored as an index
   * (not a resolved class) so the pod-label color can re-theme when the
   * viewer's isDark state toggles at runtime.
   */
  podColorIndex?: number
  level: LogLevel
  levelSource: LevelSource
  isJson: boolean
  isLogfmt: boolean
}

const MAX_BUFFER_SIZE = 10_000

function isJsonContent(content: string): boolean {
  const trimmed = content.trimStart()
  return trimmed[0] === '{' && trimmed[trimmed.length - 1] === '}'
}

type RawLogEntry = Omit<LogEntry, 'id' | 'level' | 'levelSource' | 'isJson' | 'isLogfmt'>

interface UseLogBufferReturn {
  entries: LogEntry[]
  append: (entry: RawLogEntry) => void
  appendBatch: (entries: RawLogEntry[]) => void
  set: (entries: RawLogEntry[]) => void
  clear: () => void
}

export function useLogBuffer(): UseLogBufferReturn {
  const [entries, setEntries] = useState<LogEntry[]>([])
  const idCounter = useRef(0)
  const pendingRef = useRef<RawLogEntry[]>([])
  const rafRef = useRef<number | null>(null)

  const enrichEntry = useCallback((raw: RawLogEntry): LogEntry => {
    const isJ = isJsonContent(raw.content)
    const detected = detectLevel(raw.content)
    return {
      ...raw,
      id: idCounter.current++,
      level: detected.level,
      levelSource: detected.source,
      isJson: isJ,
      isLogfmt: !isJ && isLogfmt(raw.content),
    }
  }, [])

  const flushPending = useCallback(() => {
    rafRef.current = null
    const batch = pendingRef.current
    if (batch.length === 0) return
    pendingRef.current = []

    setEntries(prev => {
      const enriched = batch.map(enrichEntry)
      const combined = [...prev, ...enriched]
      if (combined.length > MAX_BUFFER_SIZE) {
        return combined.slice(combined.length - MAX_BUFFER_SIZE)
      }
      return combined
    })
  }, [enrichEntry])

  const append = useCallback((entry: RawLogEntry) => {
    pendingRef.current.push(entry)
    if (rafRef.current === null) {
      rafRef.current = requestAnimationFrame(flushPending)
    }
  }, [flushPending])

  const appendBatch = useCallback((batch: RawLogEntry[]) => {
    pendingRef.current.push(...batch)
    if (rafRef.current === null) {
      rafRef.current = requestAnimationFrame(flushPending)
    }
  }, [flushPending])

  const set = useCallback((rawEntries: RawLogEntry[]) => {
    // Cancel any pending RAF
    if (rafRef.current !== null) {
      cancelAnimationFrame(rafRef.current)
      rafRef.current = null
    }
    pendingRef.current = []

    const enriched = rawEntries.map(enrichEntry)
    if (enriched.length > MAX_BUFFER_SIZE) {
      setEntries(enriched.slice(enriched.length - MAX_BUFFER_SIZE))
    } else {
      setEntries(enriched)
    }
  }, [enrichEntry])

  const clear = useCallback(() => {
    if (rafRef.current !== null) {
      cancelAnimationFrame(rafRef.current)
      rafRef.current = null
    }
    pendingRef.current = []
    setEntries([])
  }, [])

  return { entries, append, appendBatch, set, clear }
}
