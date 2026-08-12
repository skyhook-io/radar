import { useState, useCallback, useEffect } from 'react'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'

export interface DefaultSort {
  column: string    // resource-table column key, e.g. "age", "name", "status"
  direction: 'asc' | 'desc'
}

const STORAGE_KEY = 'radar-default-sort'

// The preference has two live consumers in one tab — the Settings dialog and the
// resource table, which writes back on every manual sort. Without a shared
// subscriber list each would hold its own stale copy until a reload.
type Listener = (sort: DefaultSort | null) => void
const listeners = new Set<Listener>()
// One fetch per page load, not one per mounted consumer.
let serverSyncStarted = false

// Distinguishes "never chose" (null) from "chose nothing" (the string 'null'),
// which is what makes clearing the preference stick — see the sync effect.
function readStored(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

function loadDefaultSort(): DefaultSort | null {
  try {
    const raw = readStored()
    if (raw) {
      const parsed = JSON.parse(raw)
      if (parsed?.column && (parsed.direction === 'asc' || parsed.direction === 'desc')) return parsed
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
      localStorage.setItem(STORAGE_KEY, 'null')
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

  useEffect(() => {
    listeners.add(setDefaultSortState)
    return () => { listeners.delete(setDefaultSortState) }
  }, [])

  // Server sync: settings.json outlives localStorage (desktop port changes, a
  // second browser). "No preference" is a real choice, so a stored `null` wins
  // over the server copy — otherwise clearing the sort would come back on
  // reload. Only a never-written key falls through to the server.
  useEffect(() => {
    if (serverSyncStarted || readStored() !== null) return
    serverSyncStarted = true
    fetch(apiUrl('/settings'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then((res) => res.ok ? res.json() : null)
      .then((data) => {
        const sort = data?.defaultSort
        if (sort?.column && (sort.direction === 'asc' || sort.direction === 'desc')) {
          try {
            localStorage.setItem(STORAGE_KEY, JSON.stringify(sort))
          } catch {
            // ignore storage errors
          }
          listeners.forEach((l) => l(sort))
        }
      })
      .catch((err) => console.warn('[settings] Failed to load defaultSort from server:', err))
  }, [])

  const setDefaultSort = useCallback((sort: DefaultSort | null) => {
    saveDefaultSort(sort)
    listeners.forEach((l) => l(sort))
  }, [])

  return { defaultSort, setDefaultSort }
}
