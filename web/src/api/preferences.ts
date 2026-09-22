import { apiUrl, getApiBase, getAuthHeaders, getCredentialsMode } from './config'
import type { PinnedKind } from '../hooks/useFavorites'

interface Preferences {
  theme?: 'light' | 'dark'
  pinnedKinds?: PinnedKind[]
  preferenceStorage?: 'browser'
}

let request: { base: string; promise: Promise<Preferences> } | undefined

export function loadPreferences(): Promise<Preferences> {
  const base = getApiBase()
  if (!request || request.base !== base) {
    const promise = fetch(apiUrl('/settings'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then((res) => {
        if (!res.ok) throw new Error(`Failed to load preferences: HTTP ${res.status}`)
        return res.json() as Promise<Preferences>
      })
    request = { base, promise }
    promise.catch(() => { if (request?.promise === promise) request = undefined })
  }
  return request.promise
}

export async function persistPreferences(patch: Pick<Preferences, 'theme' | 'pinnedKinds'>): Promise<void> {
  const base = getApiBase()
  const preferences = await loadPreferences()
  if (base !== getApiBase() || preferences.preferenceStorage === 'browser') return
  const res = await fetch(apiUrl('/settings'), {
    method: 'PUT',
    credentials: getCredentialsMode(),
    headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
    body: JSON.stringify(patch),
  })
  if (!res.ok) throw new Error(`Failed to save preferences: HTTP ${res.status}`)
  if (request?.base === base) request = undefined
}
