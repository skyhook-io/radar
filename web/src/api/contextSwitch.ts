import { createTabStorage } from '../utils/tabStorage'
import { getApiBase } from './config'

export interface ContextSwitchOperation {
  id: string
  target: string
  phase: 'preflight' | 'switching' | 'succeeded' | 'failed' | 'cancelled'
  startedAt: string
  progress: string
  error?: string
  errorType?: string
}

export function switchInProgress(op?: ContextSwitchOperation) {
  return op?.phase === 'preflight' || op?.phase === 'switching'
}

// Only present the terminal result in a tab that was following this operation.
// A later visit also receives the server's last completed operation.
const tab = createTabStorage()
export function followingSwitch() {
  return tab.get('radar:context-switch:' + getApiBase())
}
export function followSwitch(id: string) {
  tab.set('radar:context-switch:' + getApiBase(), id)
}
