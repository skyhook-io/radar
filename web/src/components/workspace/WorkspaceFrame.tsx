import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { useLocation } from 'react-router-dom'
import { DialogPortal, ResourcesSidebar, type SelectedKindInfo, type SidebarCategoryWorkspace } from '@skyhook-io/k8s-ui'
import { PanelLeft, X } from 'lucide-react'
import { useAPIResources } from '../../api/apiResources'
import { usePinnedKinds } from '../../hooks/useFavorites'
import { useResourceCounts } from '../../hooks/useResourceCounts'
import { useWorkspaceNavigate } from './useWorkspaceNavigate'

/**
 * A workspace inside Resources: the same Resources sidebar, with the
 * workspaces' destinations above their exact kinds, and the screen beside it.
 * On narrow windows the sidebar moves into a dialog behind a button.
 */
export function WorkspaceFrame({
  namespaces,
  categoryWorkspaces,
  selectedKind = null,
  compactNavigation,
  children,
}: {
  namespaces: string[]
  categoryWorkspaces: Record<string, SidebarCategoryWorkspace> | undefined
  /** The kind whose row is highlighted, when the screen is one object of it rather than a destination. */
  selectedKind?: SelectedKindInfo | null
  compactNavigation: boolean
  children: ReactNode
}) {
  const location = useLocation()
  const navigate = useWorkspaceNavigate()
  const [navigationOpen, setNavigationOpen] = useState(false)
  useEffect(() => setNavigationOpen(false), [location.pathname, location.search, compactNavigation])
  const { data: apiResources } = useAPIResources()
  const { data: counts } = useResourceCounts(namespaces)
  const { pinned, togglePin, isPinned } = usePinnedKinds()

  const selectKind = useCallback(
    (kind: SelectedKindInfo) => {
      navigate(`/resources/${kind.name}${kind.group ? `?apiGroup=${encodeURIComponent(kind.group)}` : ''}`)
    },
    [navigate],
  )

  const sidebar = <ResourcesSidebar
        selectedKind={selectedKind}
        onSelectedKindChange={selectKind}
        apiResources={apiResources}
        resourceCounts={counts?.counts}
        resourceCoverage={counts?.coverage}
        resourceForbidden={counts?.forbidden}
        resourceUnavailable={counts?.unavailable}
        pinned={pinned}
        togglePin={togglePin}
        isPinned={(kind: string, group?: string) => isPinned(kind, group ?? '')}
        categoryWorkspaces={categoryWorkspaces}
        className={compactNavigation ? '!w-full !border-r-0' : undefined}
      />
  return (
    <div className="flex h-full min-h-0 w-full">
      {!compactNavigation && sidebar}
      <DialogPortal open={navigationOpen && compactNavigation} onClose={() => setNavigationOpen(false)} ariaLabel="Resource navigation" className="flex h-[min(80vh,44rem)] w-full max-w-md flex-col overflow-hidden">
        <div className="flex items-center justify-between border-b border-theme-border px-4 py-3"><h2 className="font-medium text-theme-text-primary">Resources</h2><button type="button" aria-label="Close resource navigation" onClick={() => setNavigationOpen(false)} className="rounded p-1 text-theme-text-secondary hover:bg-theme-hover"><X className="h-4 w-4" /></button></div>
        <div className="flex min-h-0 flex-1">{sidebar}</div>
      </DialogPortal>
      <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
        {compactNavigation && <div className="border-b border-theme-border px-5 py-2"><button type="button" aria-haspopup="dialog" aria-expanded={navigationOpen} onClick={() => setNavigationOpen(true)} className="btn-secondary inline-flex items-center gap-1.5 px-2.5 py-1 text-xs"><PanelLeft className="h-3.5 w-3.5" />Resources</button></div>}
        {children}
      </div>
    </div>
  )
}
