import { ResourceDetailDrawer as BaseResourceDetailDrawer } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import { WorkloadView } from '../workload/WorkloadView'

interface ResourceDetailDrawerProps {
  resource: SelectedResource
  onClose: () => void
  onNavigate?: (resource: SelectedResource) => void
  /** Open directly to YAML view */
  initialTab?: 'detail' | 'yaml'
  /** Controls slide-in/out animation (driven by useAnimatedUnmount) */
  isOpen?: boolean
  /** Whether the drawer is expanded to full-screen WorkloadView */
  expanded?: boolean
  /** Called when user clicks collapse in expanded mode */
  onCollapse?: () => void
  /** Called when user clicks expand button */
  onExpand?: (resource: SelectedResource) => void
  /** Navigate to another resource within expanded WorkloadView */
  onNavigateToResource?: (resource: SelectedResource) => void
}

export function ResourceDetailDrawer(props: ResourceDetailDrawerProps) {
  return (
    <BaseResourceDetailDrawer {...props}>
      {({ resource, expanded, initialTab, onClose, onExpand, onBack, onNavigateToResource, onCollapseToDrawer }) => (
        <WorkloadView
          kind={resource.kind}
          namespace={resource.namespace}
          name={resource.name}
          group={resource.group}
          expanded={expanded}
          initialTab={initialTab}
          onClose={onClose}
          onExpand={onExpand}
          onBack={onBack ?? (() => {})}
          onNavigateToResource={onNavigateToResource}
          onCollapseToDrawer={onCollapseToDrawer}
        />
      )}
    </BaseResourceDetailDrawer>
  )
}
