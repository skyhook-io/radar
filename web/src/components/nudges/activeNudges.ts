import { useEffect, useSyncExternalStore } from 'react'

// Corner nudges (the star callout, the update notice, the poll) each mark
// themselves visible here. Low-priority nudges read it and stay away while
// anything else is showing, so a person never gets two asks at once.
const active = new Set<string>()
// Nudges seen since this page loaded, visible or not now. The poll stays away
// for the rest of a load in which the star callout appeared.
const seenThisLoad = new Set<string>()
const listeners = new Set<() => void>()
let version = 0

function emit() {
  version++
  listeners.forEach((l) => l())
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function setNudgeVisible(id: string, visible: boolean) {
  const had = active.has(id)
  if (visible) seenThisLoad.add(id)
  if (visible && !had) active.add(id)
  else if (!visible && had) active.delete(id)
  else return
  emit()
}

/** Marks a nudge visible while `visible` is true, and clears it on unmount. */
export function useNudgeVisible(id: string, visible: boolean) {
  useEffect(() => {
    setNudgeVisible(id, visible)
    return () => setNudgeVisible(id, false)
  }, [id, visible])
}

/** True when any nudge other than `self` is showing. */
export function otherNudgeVisible(self?: string): boolean {
  for (const id of active) if (id !== self) return true
  return false
}

export function useOtherNudgeVisible(self?: string): boolean {
  useSyncExternalStore(subscribe, () => version)
  return otherNudgeVisible(self)
}

export function nudgeSeenThisLoad(id: string): boolean {
  return seenThisLoad.has(id)
}

/** Test-only reset. */
export function resetNudgesForTest() {
  active.clear()
  seenThisLoad.clear()
  emit()
}
