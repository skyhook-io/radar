import { getApiBase } from './config'

const STORAGE_KEY_PREFIX = 'radar-browser-update-check'

export function utcDay(now: Date): string {
  return now.toISOString().slice(0, 10)
}

export function claimDailyUpdateCheck(
  storage: Pick<Storage, 'getItem' | 'setItem'>,
  scope: string,
  now: Date,
): string | null {
  const storageKey = `${STORAGE_KEY_PREFIX}:${scope}`
  const day = utcDay(now)
  if (storage.getItem(storageKey) === day) return null

  storage.setItem(storageKey, day)
  return day
}

export function claimBrowserUpdateCheck(): string | null {
  try {
    return claimDailyUpdateCheck(localStorage, getApiBase(), new Date())
  } catch {
    return null
  }
}
