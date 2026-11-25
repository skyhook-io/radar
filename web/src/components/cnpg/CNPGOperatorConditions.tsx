import { WorkspaceConditions } from '../workspace/WorkspaceConditions'
import type { ComponentProps } from 'react'

export function CNPGOperatorConditions(props: Pick<ComponentProps<typeof WorkspaceConditions>, 'conditions'>) {
  return <WorkspaceConditions {...props} className="mx-4 mb-4" emptyMessage="The operator has not reported status.conditions." />
}
