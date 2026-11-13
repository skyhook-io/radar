// Which workspace queries a live resource change makes stale. App.tsx collects
// the due entries while it batches events and invalidates each once per batch.
export interface WorkspaceEvent { kind: string; group?: string; operation?: 'add' | 'update' | 'delete' }

export interface WorkspaceInvalidation {
  id: string
  matches: (event: WorkspaceEvent) => boolean
  queryKey: readonly unknown[]
}

export const workspaceInvalidations: WorkspaceInvalidation[] = [
  { id: 'cnpg', matches: e => !!e.group?.endsWith('.cnpg.io'), queryKey: ['cnpg', 'workspace'] },
  // Pods coming and going move the pipeline's counts. Status churn doesn't
  // trigger a full pod rescan; the mounted page polls for that.
  { id: 'logging', matches: e => e.group === 'logging.banzaicloud.io' || (e.kind === 'Pod' && !e.group && e.operation !== 'update'), queryKey: ['logging', 'pipeline'] },
]

export function pendingInvalidations(events: WorkspaceEvent[]): WorkspaceInvalidation[] {
  return workspaceInvalidations.filter(w => events.some(e => w.matches(e)))
}
