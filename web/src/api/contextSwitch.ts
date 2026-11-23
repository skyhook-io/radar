import { useSyncExternalStore } from 'react'
import { createTabStorage } from '../utils/tabStorage'
import { getApiBase } from './config'

export interface ContextSwitchOperation {
  id: string
  target: string
  phase: 'preflight' | 'switching' | 'stopping' | 'stopped' | 'succeeded' | 'failed' | 'cancelled'
  startedAt: string
  preflightTimeoutSeconds?: number
  syncTimeoutSeconds: number
  progress: string
  canStop?: boolean
  error?: string
  errorType?: string
}

export function switchInProgress(op?: ContextSwitchOperation) {
  return op?.phase === 'preflight' || op?.phase === 'switching' || op?.phase === 'stopping'
}

// Only present the terminal result in a tab that was following this operation.
// A later visit also receives the server's last completed operation.
const tab = createTabStorage()
const listeners = new Set<() => void>()
function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function followingSwitch() {
  return tab.get('radar:context-switch:' + getApiBase())
}
// Operations this tab presented a result for. A late acknowledgement of one
// (its POST outlived the poll that showed the outcome) must not replay it.
const finished = new Map<string, Set<string>>()

export function followSwitch(id: string) {
  if (switchFinished(id)) return
  tab.set('radar:context-switch:' + getApiBase(), id)
  listeners.forEach(listener => listener())
}

/** Whether this tab already presented the result of this operation. */
export function switchFinished(id: string) {
  return Boolean(id) && Boolean(finished.get(getApiBase())?.has(id))
}

export function finishSwitch(id: string) {
  const base = getApiBase()
  finished.set(base, (finished.get(base) ?? new Set()).add(id))
  followSwitch('')
}

export function useTrackedContextSwitch(operation?: ContextSwitchOperation) {
  const followedId = useSyncExternalStore(subscribe, followingSwitch, followingSwitch)
  return switchInProgress(operation) || operation?.id === followedId ? operation : undefined
}
