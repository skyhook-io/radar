import { getApiBase } from './config'

const STORAGE_KEY_PREFIX = 'radar-browser-update-check'

export function utcDay(now: Date): string {
  return now.toISOString().slice(0, 10)
}

export function markDailyUpdateCheckAttempt(
  storage: Pick<Storage, 'getItem' | 'setItem'>,
  apiBase: string,
  now: Date,
): boolean {
  const storageKey = `${STORAGE_KEY_PREFIX}:${apiBase}`
  const day = utcDay(now)
  try {
    if (storage.getItem(storageKey) === day) return false

    storage.setItem(storageKey, day)
    return true
  } catch {
    return false
  }
}

export function markBrowserUpdateCheckAttempt(): boolean {
  try {
    return markDailyUpdateCheckAttempt(localStorage, getApiBase(), new Date())
  } catch {
    return false
  }
}
