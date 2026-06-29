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
  /** Called when user clicks expand button (opts.yaml = expanding from YAML view) */
  onExpand?: (resource: SelectedResource, opts?: { yaml?: boolean }) => void
  /** Hide the collapse-to-drawer control (mobile: no drawer to collapse to). Default true. */
  canCollapseToDrawer?: boolean
  /** Navigate to another resource within expanded WorkloadView */
  onNavigateToResource?: (resource: SelectedResource) => void
  /** Top offset for the drawer (px). Defaults to Radar's 49px header height;
   *  pass 0 in chromeless embeds where there's no Radar header above it. */
  headerHeight?: number
}

export function ResourceDetailDrawer(props: ResourceDetailDrawerProps) {
  return (
    <BaseResourceDetailDrawer {...props}>
      {({ resource, expanded, active, initialTab, onClose, onExpand, onExpandIntent, onCancelExpandIntent, onBack, onNavigateToResource, onCollapseToDrawer }) => (
        <WorkloadView
          kind={resource.kind}
          namespace={resource.namespace}
          name={resource.name}
          group={resource.group}
          expanded={expanded}
          active={active}
          initialTab={initialTab}
          onClose={onClose}
          onExpand={onExpand}
          onExpandIntent={onExpandIntent}
          onCancelExpandIntent={onCancelExpandIntent}
          onBack={onBack ?? (() => {})}
          onNavigateToResource={onNavigateToResource}
          onCollapseToDrawer={onCollapseToDrawer}
        />
      )}
    </BaseResourceDetailDrawer>
  )
}
