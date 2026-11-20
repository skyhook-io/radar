import { kindToPluralWithGroup, type IssueResourceRef } from '@skyhook-io/k8s-ui'
import { recordUsageEvent } from '../../api/usage-data'
import type { NavigateToResource } from '../../utils/navigation'

// Opens a resource linked from an issue or finding, and counts it for usage data.
export function issueResourceOpener(navigate: NavigateToResource | undefined) {
  if (!navigate) return undefined
  return (ref: IssueResourceRef) => {
    recordUsageEvent({ type: 'ui', name: 'issue_action:open' })
    navigate({
      kind: kindToPluralWithGroup(ref.kind, ref.group ?? ''),
      namespace: ref.namespace ?? '',
      name: ref.name,
      group: ref.group ?? '',
    })
  }
}
