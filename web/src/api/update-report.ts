import { getApiBase } from './config'

const STORAGE_KEY_PREFIX = 'radar-browser-update-check'

export function utcDay(now: Date): string {
  return now.toISOString().slice(0, 10)
}

export function claimDailyUpdateCheck(
  storage: Pick<Storage, 'getItem' | 'setItem'>,
  apiBase: string,
  now: Date,
  installScope?: string,
): string | null {
  const storageKey = `${STORAGE_KEY_PREFIX}:${apiBase}`
  const day = utcDay(now)
  const storedValue = storage.getItem(storageKey)
  const stored = storedValue
    ? JSON.parse(storedValue) as { day?: string; installScope?: string }
    : undefined
  const sameInstallation = !stored?.installScope
    || !installScope
    || stored.installScope === installScope
  if (stored?.day === day && sameInstallation) return null

  storage.setItem(storageKey, JSON.stringify({ day, installScope }))
  return day
}

export function claimBrowserUpdateCheck(installScope?: string): string | null {
  try {
    return claimDailyUpdateCheck(localStorage, getApiBase(), new Date(), installScope)
  } catch {
    return null
  }
}
