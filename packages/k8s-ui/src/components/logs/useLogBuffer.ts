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
  isFollowing: boolean
  retainedLineCount: number
  missingLineCount: number
  getRetainedEntries: () => LogEntry[]
  pendingLineCount: number
  setFollowing: (following: boolean) => void
  append: (entry: RawLogEntry) => void
  appendBatch: (entries: RawLogEntry[]) => void
  set: (entries: RawLogEntry[]) => void
  clear: () => void
}

export function useLogBuffer(): UseLogBufferReturn {
  const [buffer, setBuffer] = useState<{ latest: LogEntry[]; reading: LogEntry[] | null }>({ latest: [], reading: null })
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
    const enriched = batch.map(enrichEntry)

    setBuffer(prev => {
      const combined = [...prev.latest, ...enriched]
      const latest = combined.length > MAX_BUFFER_SIZE ? combined.slice(-MAX_BUFFER_SIZE) : combined
      // Keep appending while the reader's window fits. Once eviction begins,
      // retain that window rather than replacing the lines being read.
      const reading = prev.reading === null ? null
        : prev.reading === prev.latest && combined.length <= MAX_BUFFER_SIZE ? latest : prev.reading
      return { latest, reading }
    })
  }, [enrichEntry])

  const append = useCallback((entry: RawLogEntry) => {
    pendingRef.current.push(entry)
    // Hidden tabs can stop delivering animation frames while logs still arrive.
    if (pendingRef.current.length >= MAX_BUFFER_SIZE) {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current)
      flushPending()
      return
    }
    if (rafRef.current === null) {
      rafRef.current = requestAnimationFrame(flushPending)
    }
  }, [flushPending])

  const appendBatch = useCallback((batch: RawLogEntry[]) => {
    for (const entry of batch) append(entry)
  }, [append])

  const set = useCallback((rawEntries: RawLogEntry[]) => {
    // Cancel any pending RAF
    if (rafRef.current !== null) {
      cancelAnimationFrame(rafRef.current)
      rafRef.current = null
    }
    pendingRef.current = []

    const enriched = rawEntries.map(enrichEntry)
    if (enriched.length > MAX_BUFFER_SIZE) {
      setBuffer({ latest: enriched.slice(enriched.length - MAX_BUFFER_SIZE), reading: null })
    } else {
      setBuffer({ latest: enriched, reading: null })
    }
  }, [enrichEntry])

  const clear = useCallback(() => {
    if (rafRef.current !== null) {
      cancelAnimationFrame(rafRef.current)
      rafRef.current = null
    }
    pendingRef.current = []
    setBuffer({ latest: [], reading: null })
  }, [])

  const setFollowing = useCallback((following: boolean) => {
    setBuffer(prev => {
      if (following) return prev.reading === null ? prev : { ...prev, reading: null }
      return prev.reading === null ? { ...prev, reading: prev.latest } : prev
    })
  }, [])

  const entries = buffer.reading ?? buffer.latest
  const pendingLineCount = (buffer.latest.at(-1)?.id ?? -1) - (entries.at(-1)?.id ?? -1)
  const readingLastId = entries.at(-1)?.id ?? -1
  const latestFirstId = buffer.latest[0]?.id ?? 0
  const overlap = Math.max(0, readingLastId - latestFirstId + 1)
  const retainedLineCount = buffer.reading === null ? buffer.latest.length : entries.length + buffer.latest.length - overlap
  const missingLineCount = buffer.reading === null ? 0 : Math.max(0, latestFirstId - readingLastId - 1)
  const getRetainedEntries = useCallback(() => {
    if (buffer.reading === null || buffer.reading === buffer.latest) return buffer.latest
    const lastId = buffer.reading.at(-1)?.id ?? -1
    return [...buffer.reading, ...buffer.latest.filter(entry => entry.id > lastId)]
  }, [buffer])
  return { entries, retainedLineCount, missingLineCount, getRetainedEntries, isFollowing: buffer.reading === null, pendingLineCount, setFollowing, append, appendBatch, set, clear }
}
