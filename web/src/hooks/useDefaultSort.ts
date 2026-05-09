import { useState, useCallback, useEffect } from 'react'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'

export interface DefaultSort {
  column: string    // e.g. "age", "name", "status", "cpu", "memory"
  direction: 'asc' | 'desc'
}

const STORAGE_KEY = 'radar-default-sort'

// Module-level subscribers so all hook instances stay in sync within the same tab
type Listener = (sort: DefaultSort | null) => void
const listeners = new Set<Listener>()

function loadDefaultSort(): DefaultSort | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const parsed = JSON.parse(raw)
      if (parsed?.column && parsed?.direction) return parsed
    }
  } catch {
    // ignore parse errors
  }
  return null
}

function saveDefaultSort(sort: DefaultSort | null) {
  try {
    if (sort) {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(sort))
    } else {
      localStorage.removeItem(STORAGE_KEY)
    }
  } catch {
    // ignore storage errors
  }
  fetch(apiUrl('/settings'), {
    method: 'PUT',
    credentials: getCredentialsMode(),
    headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
    body: JSON.stringify({ defaultSort: sort }),
  })
    .then((res) => { if (!res.ok) console.warn('[settings] Failed to persist defaultSort:', res.status) })
    .catch((err) => console.warn('[settings] Failed to persist defaultSort:', err))
}

export function useDefaultSort() {
  const [defaultSort, setDefaultSortState] = useState<DefaultSort | null>(loadDefaultSort)

  // Register this instance so other callers can push updates to it
  useEffect(() => {
    listeners.add(setDefaultSortState)
    return () => { listeners.delete(setDefaultSortState) }
  }, [])

  // Sync from server (persisted settings survive port changes in desktop app)
  useEffect(() => {
    fetch(apiUrl('/settings'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then((res) => res.ok ? res.json() : null)
      .then((data) => {
        if (data?.defaultSort?.column && data?.defaultSort?.direction && !loadDefaultSort()) {
          setDefaultSortState(data.defaultSort)
          localStorage.setItem(STORAGE_KEY, JSON.stringify(data.defaultSort))
        }
      })
      .catch((err) => console.warn('[settings] Failed to load defaultSort from server:', err))
  }, [])

  const setDefaultSort = useCallback((sort: DefaultSort | null) => {
    saveDefaultSort(sort)
    listeners.forEach(l => l(sort))
  }, [])

  return { defaultSort, setDefaultSort }
}
