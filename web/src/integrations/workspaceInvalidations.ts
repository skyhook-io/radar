// Which workspace queries a live resource change makes stale. App.tsx collects
// the due entries while it batches events and invalidates each once per batch.
export interface WorkspaceInvalidation {
  id: string
  matches: (event: { kind: string; group?: string }) => boolean
  queryKey: readonly unknown[]
}

export const workspaceInvalidations: WorkspaceInvalidation[] = [
  { id: 'cnpg', matches: e => !!e.group?.endsWith('.cnpg.io'), queryKey: ['cnpg', 'workspace'] },
]

export function pendingInvalidations(events: { kind: string; group?: string }[]): WorkspaceInvalidation[] {
  return workspaceInvalidations.filter(w => events.some(e => w.matches(e)))
}
