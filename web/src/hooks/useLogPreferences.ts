import { useState, useCallback, useEffect } from 'react'

const WRAP_KEY = 'radar-logs-wrap'
const TIMESTAMPS_KEY = 'radar-logs-timestamps'

function loadBool(key: string, defaultValue: boolean): boolean {
  try {
    const raw = localStorage.getItem(key)
    if (raw === null) return defaultValue
    return raw !== 'false'
  } catch {
    return defaultValue
  }
}

function saveBool(key: string, value: boolean) {
  try {
    localStorage.setItem(key, String(value))
  } catch {
    // ignore storage errors (e.g., Safari private mode)
  }
}

export function useLogPreferences() {
  const [logsWrap, setLogsWrap] = useState(() => loadBool(WRAP_KEY, true))
  const [logsTimestamps, setLogsTimestamps] = useState(() => loadBool(TIMESTAMPS_KEY, true))

  // Sync from server on mount (persisted settings survive port changes in desktop app)
  useEffect(() => {
    fetch('/api/settings')
      .then((res) => res.ok ? res.json() : null)
      .then((data) => {
        if (!data) return
        // Only sync from server if localStorage doesn't have the key yet
        if (localStorage.getItem(WRAP_KEY) === null && data.logsWrap != null) {
          setLogsWrap(data.logsWrap)
          saveBool(WRAP_KEY, data.logsWrap)
        }
        if (localStorage.getItem(TIMESTAMPS_KEY) === null && data.logsTimestamps != null) {
          setLogsTimestamps(data.logsTimestamps)
          saveBool(TIMESTAMPS_KEY, data.logsTimestamps)
        }
      })
      .catch((err) => console.warn('[settings] Failed to load log preferences from server:', err))
  }, [])

  const setWrap = useCallback((value: boolean) => {
    setLogsWrap(value)
    saveBool(WRAP_KEY, value)
    fetch('/api/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ logsWrap: value }),
    })
      .then((res) => { if (!res.ok) console.warn('[settings] Failed to persist logsWrap:', res.status) })
      .catch((err) => console.warn('[settings] Failed to persist logsWrap:', err))
  }, [])

  const setTimestamps = useCallback((value: boolean) => {
    setLogsTimestamps(value)
    saveBool(TIMESTAMPS_KEY, value)
    fetch('/api/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ logsTimestamps: value }),
    })
      .then((res) => { if (!res.ok) console.warn('[settings] Failed to persist logsTimestamps:', res.status) })
      .catch((err) => console.warn('[settings] Failed to persist logsTimestamps:', err))
  }, [])

  return { logsWrap, logsTimestamps, setLogsWrap: setWrap, setLogsTimestamps: setTimestamps }
}
