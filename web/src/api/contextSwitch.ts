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
export function followSwitch(id: string) {
  tab.set('radar:context-switch:' + getApiBase(), id)
  listeners.forEach(listener => listener())
}

export function useTrackedContextSwitch(operation?: ContextSwitchOperation) {
  const followedId = useSyncExternalStore(subscribe, followingSwitch, followingSwitch)
  return switchInProgress(operation) || operation?.id === followedId ? operation : undefined
}
