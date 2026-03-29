import { useState, useCallback, useEffect, useRef, type ReactNode } from 'react'
import { TRANSITION_DRAWER } from '../../utils/animation'
import { clsx } from 'clsx'
import type { SelectedResource } from '../../types'

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
  /** Height of the host app's top navigation bar in px (default: 49) */
  headerHeight?: number
  /** Left offset to exclude (e.g. sidebar width) so expanded mode doesn't cover the sidebar (default: 0) */
  leftOffset?: number
  /** Render the content inside the drawer */
  children: (props: {
    resource: SelectedResource
    expanded: boolean
    initialTab?: 'detail' | 'yaml'
    onClose: () => void
    onExpand?: () => void
    onBack?: () => void
    onNavigateToResource?: (resource: SelectedResource) => void
    onCollapseToDrawer?: () => void
  }) => ReactNode
}

const MIN_WIDTH = 400
const MAX_WIDTH_PERCENT = 0.7
const DEFAULT_WIDTH = 550
const WIDE_WIDTH = 750

const WIDE_KINDS = new Set([
  'vulnerabilityreports', 'configauditreports', 'exposedsecretreports',
  'rbacassessmentreports', 'clusterrbacassessmentreports', 'clustercompliancereports',
  'sbomreports', 'clustersbomreports', 'policyreports', 'clusterpolicyreports',
])

function getDefaultWidth(kind: string): number {
  return WIDE_KINDS.has(kind.toLowerCase()) ? WIDE_WIDTH : DEFAULT_WIDTH
}

export function ResourceDetailDrawer({ resource, onClose, onNavigate, initialTab, isOpen = true, expanded, onCollapse, onExpand, onNavigateToResource, headerHeight: headerHeightProp, leftOffset = 0, children }: ResourceDetailDrawerProps) {
  const [drawerWidth, setDrawerWidth] = useState(() => getDefaultWidth(resource.kind))
  const [isResizing, setIsResizing] = useState(false)
  const resizeStartX = useRef(0)
  const resizeStartWidth = useRef(getDefaultWidth(resource.kind))

  // Detect collapse direction: was expanded last render, now not.
  // Width snaps instantly on collapse (0ms) so content and size match.
  // Expand keeps the nice 300ms width animation via TRANSITION_DRAWER.
  const wasExpanded = useRef(!!expanded)
  const isCollapsing = wasExpanded.current && !expanded
  wasExpanded.current = !!expanded

  // Reset drawer width when resource kind changes
  useEffect(() => {
    const w = getDefaultWidth(resource.kind)
    setDrawerWidth(w)
    resizeStartWidth.current = w
  }, [resource.kind])

  // Resize handlers (disabled when expanded)
  const handleResizeStart = useCallback((e: React.MouseEvent) => {
    if (expanded) return
    e.preventDefault()
    setIsResizing(true)
    resizeStartX.current = e.clientX
    resizeStartWidth.current = drawerWidth
  }, [drawerWidth, expanded])

  useEffect(() => {
    if (!isResizing) return
    document.body.style.cursor = 'ew-resize'
    document.body.style.userSelect = 'none'
    const maxWidth = window.innerWidth * MAX_WIDTH_PERCENT
    const handleMouseMove = (e: MouseEvent) => {
      const deltaX = resizeStartX.current - e.clientX
      const newWidth = resizeStartWidth.current + deltaX
      setDrawerWidth(Math.max(MIN_WIDTH, Math.min(newWidth, maxWidth)))
    }
    const handleMouseUp = () => setIsResizing(false)
    document.addEventListener('mousemove', handleMouseMove)
    document.addEventListener('mouseup', handleMouseUp)
    return () => {
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('mouseup', handleMouseUp)
    }
  }, [isResizing])

  // Route navigation based on expanded state
  const handleNavigate = useCallback((res: SelectedResource) => {
    if (expanded) {
      onNavigateToResource?.(res)
    } else {
      onNavigate?.(res)
    }
  }, [expanded, onNavigateToResource, onNavigate])

  const headerHeight = headerHeightProp ?? 49

  return (
    <div
      className={clsx(
        'fixed right-0 bg-theme-surface border-l border-theme-border flex flex-col shadow-drawer z-40',
        TRANSITION_DRAWER,
        isOpen
          ? 'translate-x-0 opacity-100'
          : 'translate-x-full opacity-0',
        expanded && '!border-l-0',
      )}
      style={{
        width: expanded ? `calc(100vw - ${leftOffset}px)` : drawerWidth,
        top: headerHeight,
        height: `calc(100vh - ${headerHeight}px)`,
        // Collapse is instant — no animation, content and width snap together.
        // Expand + slide-in/out animate via TRANSITION_DRAWER class.
        ...(isCollapsing && { transition: 'none' }),
      }}
    >
      {/* Resize handle — hidden when expanded or on mobile */}
      {!expanded && (
        <div
          onMouseDown={handleResizeStart}
          className={clsx(
            'absolute left-0 top-0 bottom-0 w-2 cursor-ew-resize z-10 hover:bg-skyhook-500/50 transition-colors',
            'hidden sm:block',
            isResizing && 'bg-skyhook-500/50'
          )}
        />
      )}

      {children({
        resource,
        expanded: !!expanded,
        initialTab,
        onClose,
        onExpand: onExpand ? () => onExpand(resource) : undefined,
        onBack: onCollapse ? () => onCollapse() : undefined,
        onNavigateToResource: handleNavigate,
        onCollapseToDrawer: onCollapse ? () => onCollapse() : undefined,
      })}
    </div>
  )
}
