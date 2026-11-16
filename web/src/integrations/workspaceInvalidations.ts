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
  // Pod events don't refetch: each refetch rescans every pod against every
  // route, and Job churn deletes pods every few seconds. The mounted page
  // polls for pod changes.
  { id: 'logging', matches: e => e.group === 'logging.banzaicloud.io', queryKey: ['logging', 'pipeline'] },
]

export function pendingInvalidations(events: WorkspaceEvent[]): WorkspaceInvalidation[] {
  return workspaceInvalidations.filter(w => events.some(e => w.matches(e)))
}
