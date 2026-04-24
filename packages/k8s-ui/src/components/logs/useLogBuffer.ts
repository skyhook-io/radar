import { useState, useRef, useCallback } from 'react'
import { isLogfmt } from '../../utils/log-format'

export type LogLevel = 'error' | 'warn' | 'info' | 'debug' | 'unknown'

export interface LogEntry {
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
  isJson: boolean
  isLogfmt: boolean
}

const MAX_BUFFER_SIZE = 10_000

/**
 * Detect log level from content using word-boundary matching.
 * For JSON logs, prefer the `level`, `severity`, or `lvl` field.
 */
export function detectLogLevel(content: string): LogLevel {
  // Fast path for JSON: check level/severity field
  const trimmed = content.trimStart()
  if (trimmed[0] === '{') {
    try {
      const obj = JSON.parse(trimmed)
      const rawLevel = obj.level ?? obj.severity ?? obj.lvl ?? ''
      // Numeric levels (pino/bunyan): 10=trace, 20=debug, 30=info, 40=warn, 50=error, 60=fatal
      if (typeof rawLevel === 'number') {
        if (rawLevel >= 50) return 'error'
        if (rawLevel >= 40) return 'warn'
        if (rawLevel >= 30) return 'info'
        return 'debug'
      }
      const lvl = String(rawLevel).toLowerCase()
      if (/^(error|err|fatal|panic|critical|crit)$/.test(lvl)) return 'error'
      if (/^(warn|warning)$/.test(lvl)) return 'warn'
      if (/^(info|information|notice)$/.test(lvl)) return 'info'
      if (/^(debug|trace|verbose)$/.test(lvl)) return 'debug'
    } catch {
      // Not valid JSON, fall through to text matching
    }
  }

  const lower = content.toLowerCase()
  if (/\b(error|fatal|panic|critical|crit|exception)\b/.test(lower)) return 'error'
  if (/\b(warn|warning)\b/.test(lower)) return 'warn'
  if (/\b(debug|trace)\b/.test(lower)) return 'debug'
  if (/\b(info)\b/.test(lower)) return 'info'
  return 'unknown'
}

function isJsonContent(content: string): boolean {
  const trimmed = content.trimStart()
  return trimmed[0] === '{' && trimmed[trimmed.length - 1] === '}'
}

type RawLogEntry = Omit<LogEntry, 'id' | 'level' | 'isJson' | 'isLogfmt'>

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
    return {
      ...raw,
      id: idCounter.current++,
      level: detectLogLevel(raw.content),
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
