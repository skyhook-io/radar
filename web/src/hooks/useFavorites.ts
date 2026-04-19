import { useState, useCallback, useEffect } from 'react'
import { apiUrl, getAuthHeaders, getCredentialsMode } from '../api/config'

export interface PinnedKind {
  name: string       // plural name for API calls, e.g. "pods", "deployments"
  kind: string       // singular display name, e.g. "Pod", "Deployment"
  group: string      // API group, e.g. "" for core, "source.toolkit.fluxcd.io" for Flux
}

const STORAGE_KEY = 'radar-pinned-kinds'

function loadPinned(): PinnedKind[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) return JSON.parse(raw)
  } catch {
    // ignore parse errors
  }
  return []
}

function savePinned(pinned: PinnedKind[]) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(pinned))
  } catch {
    // ignore storage errors
  }
  fetch(apiUrl('/settings'), { method: 'PUT', credentials: getCredentialsMode(), headers: { 'Content-Type': 'application/json', ...getAuthHeaders() }, body: JSON.stringify({ pinnedKinds: pinned }) })
    .then((res) => { if (!res.ok) console.warn('[settings] Failed to persist pinned kinds:', res.status) })
    .catch((err) => console.warn('[settings] Failed to persist pinned kinds:', err))
}

function matches(a: PinnedKind, name: string, group: string): boolean {
  return a.name === name && a.group === group
}

export function usePinnedKinds() {
  const [pinned, setPinned] = useState<PinnedKind[]>(loadPinned)

  // Sync from server (persisted settings survive port changes in desktop app)
  useEffect(() => {
    fetch(apiUrl('/settings'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then((res) => res.ok ? res.json() : null)
      .then((data) => {
        if (data?.pinnedKinds?.length && loadPinned().length === 0) {
          setPinned(data.pinnedKinds)
          localStorage.setItem(STORAGE_KEY, JSON.stringify(data.pinnedKinds))
        }
      })
      .catch((err) => console.warn('[settings] Failed to load pinned kinds from server:', err))
  }, [])

  const togglePin = useCallback((item: PinnedKind) => {
    setPinned((prev) => {
      const exists = prev.some((p) => matches(p, item.name, item.group))
      const next = exists
        ? prev.filter((p) => !matches(p, item.name, item.group))
        : [...prev, item]
      savePinned(next)
      return next
    })
  }, [])

  const isPinned = useCallback((name: string, group: string): boolean => {
    return pinned.some((p) => matches(p, name, group))
  }, [pinned])

  return { pinned, togglePin, isPinned }
}
