import { useState, useCallback, useEffect, useLayoutEffect, useReducer, useRef, type ReactNode } from 'react'
import {
  TRANSITION_DRAWER_SLIDE,
  TRANSITION_DRAWER_EXPAND,
  CSS_EASE,
  DURATION_NORMAL,
} from '../../utils/animation'
import { clsx } from 'clsx'
import type { SelectedResource } from '../../types'
import { useDockReservedHeight } from '../dock/DockContext'

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
    /** false on the outgoing layer mid-transition — suspend shortcuts/interaction */
    active: boolean
    initialTab?: 'detail' | 'yaml'
    onClose: () => void
    onExpand?: () => void
    onBack?: () => void
    onNavigateToResource?: (resource: SelectedResource) => void
    onCollapseToDrawer?: () => void
  }) => ReactNode
}

const MIN_WIDTH = 520
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

function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(
    () => typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches,
  )
  useEffect(() => {
    const mq = window.matchMedia?.('(prefers-reduced-motion: reduce)')
    if (!mq) return
    const onChange = () => setReduced(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])
  return reduced
}

export function ResourceDetailDrawer({ resource, onClose, onNavigate, initialTab, isOpen = true, expanded, onCollapse, onExpand, onNavigateToResource, headerHeight: headerHeightProp, leftOffset = 0, children }: ResourceDetailDrawerProps) {
  const [drawerWidth, setDrawerWidth] = useState(() => getDefaultWidth(resource.kind))
  const [isResizing, setIsResizing] = useState(false)
  const resizeStartX = useRef(0)
  const resizeStartWidth = useRef(getDefaultWidth(resource.kind))
  const containerRef = useRef<HTMLDivElement>(null)
  const prefersReducedMotion = usePrefersReducedMotion()

  // Expand/collapse crossfade. During the window we mount BOTH the drawer and
  // full-screen layouts, each pinned to its own width (so neither reflows or
  // squashes), and crossfade opacity while the container width (the frame)
  // animates between them.
  //
  // `settledExpanded` is the last finished state. While it differs from the
  // incoming `expanded` prop we're mid-transition: render both layers, fade
  // `crossfadeArmed` 0→1 once a start frame has painted, then settle.
  const settledExpanded = useRef(!!expanded)
  const [crossfadeArmed, setCrossfadeArmed] = useState(false)
  const [fullWidthPx, setFullWidthPx] = useState<number | null>(null)
  const [, forceSettle] = useReducer((c: number) => c + 1, 0)
  const transitioning = settledExpanded.current !== !!expanded

  useLayoutEffect(() => {
    if (settledExpanded.current === !!expanded) return
    if (prefersReducedMotion) {
      settledExpanded.current = !!expanded
      forceSettle()
      return
    }
    // Pin the full-screen layer to the measured expanded width so its content
    // lays out at its final width from the first frame (no live reflow).
    const el = containerRef.current
    const parent = el?.offsetParent as HTMLElement | null
    const measured = parent ? parent.clientWidth - leftOffset : el?.clientWidth
    if (measured && measured > 0) setFullWidthPx(measured)
    // Pre-mount: render both layers (incoming invisible, frame held at its start
    // width) for a couple of frames so the heavy incoming WorkloadView pays its
    // mount + first-paint cost BEFORE the motion starts — otherwise that work
    // lands as dropped frames in the first third of the animation. Only after it
    // has painted (double rAF) do we arm the width + crossfade.
    setCrossfadeArmed(false)
    let raf2 = 0
    let timer = 0
    const raf1 = requestAnimationFrame(() => {
      raf2 = requestAnimationFrame(() => {
        setCrossfadeArmed(true)
        timer = window.setTimeout(() => {
          settledExpanded.current = !!expanded
          setCrossfadeArmed(false)
          forceSettle()
        }, DURATION_NORMAL)
      })
    })
    return () => {
      cancelAnimationFrame(raf1)
      cancelAnimationFrame(raf2)
      clearTimeout(timer)
    }
  }, [expanded, prefersReducedMotion, leftOffset])

  // Reset drawer width when resource kind changes
  useEffect(() => {
    const w = getDefaultWidth(resource.kind)
    setDrawerWidth(w)
    resizeStartWidth.current = w
  }, [resource.kind])

  // Resize handlers (disabled when expanded or mid-transition)
  const handleResizeStart = useCallback((e: React.MouseEvent) => {
    if (expanded || transitioning) return
    e.preventDefault()
    setIsResizing(true)
    resizeStartX.current = e.clientX
    resizeStartWidth.current = drawerWidth
  }, [drawerWidth, expanded, transitioning])

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
  const dockInset = useDockReservedHeight()

  // Width animates during expand/collapse, but snaps during manual resize and
  // when the user prefers reduced motion.
  const animateWidth = !isResizing && !prefersReducedMotion

  // During the pre-mount frames (transitioning but not yet armed) hold the frame
  // at its START width so the width transition fires only once the incoming
  // layer has painted; otherwise it snaps to the target.
  const fullW = `calc(100% - ${leftOffset}px)`
  const startWidth = settledExpanded.current ? fullW : drawerWidth
  const targetWidth = expanded ? fullW : drawerWidth
  const containerWidth = transitioning && !crossfadeArmed ? startWidth : targetWidth

  const renderLayer = (layerExpanded: boolean, active: boolean) =>
    children({
      resource,
      expanded: layerExpanded,
      active,
      initialTab,
      onClose,
      onExpand: onExpand ? () => onExpand(resource) : undefined,
      onBack: onCollapse ? () => onCollapse() : undefined,
      onNavigateToResource: handleNavigate,
      onCollapseToDrawer: onCollapse ? () => onCollapse() : undefined,
    })

  // While transitioning render both layers (outgoing = settled state, incoming
  // = target). Keying by expanded-ness keeps the incoming layer's React
  // identity stable into idle, so it survives (state intact) while only the
  // outgoing layer unmounts when the window ends.
  const layerExpandedValues = transitioning ? [true, false] : [!!expanded]

  return (
    <div
      ref={containerRef}
      className={clsx(
        'absolute right-0 bg-theme-surface border-l border-theme-border flex flex-col shadow-drawer z-40',
        // Clip the wider layer only while morphing; keep overflow visible at idle
        // so drawer popovers/tooltips aren't clipped.
        transitioning && 'overflow-hidden',
        animateWidth ? TRANSITION_DRAWER_EXPAND : TRANSITION_DRAWER_SLIDE,
        isOpen
          ? 'translate-x-0 opacity-100'
          : 'translate-x-full opacity-0',
        expanded && '!border-l-0',
      )}
      style={{
        width: containerWidth,
        top: headerHeight,
        height: `calc(100% - ${headerHeight}px - ${dockInset}px)`,
      }}
    >
      {/* Resize handle — hidden when expanded or mid-transition or on mobile */}
      {!expanded && !transitioning && (
        <div
          onMouseDown={handleResizeStart}
          className={clsx(
            'absolute left-0 top-0 bottom-0 w-2 cursor-ew-resize z-10 hover:bg-skyhook-500/50 transition-colors',
            'hidden sm:block',
            isResizing && 'bg-skyhook-500/50'
          )}
        />
      )}

      {layerExpandedValues.map((layerExpanded) => {
        if (!transitioning) {
          // Idle: a single layer fills the container (width tracks the frame so
          // manual resize works).
          return (
            <div key={layerExpanded ? 'expanded' : 'collapsed'} className="absolute inset-0">
              {renderLayer(layerExpanded, true)}
            </div>
          )
        }
        const isIncoming = layerExpanded === !!expanded
        return (
          <div
            key={layerExpanded ? 'expanded' : 'collapsed'}
            className="absolute top-0 bottom-0 right-0 overflow-hidden pointer-events-none"
            style={{
              width: layerExpanded ? (fullWidthPx ?? `calc(100% - ${leftOffset}px)`) : drawerWidth,
              opacity: isIncoming ? (crossfadeArmed ? 1 : 0) : (crossfadeArmed ? 0 : 1),
              transition: `opacity ${DURATION_NORMAL}ms ${CSS_EASE}`,
              willChange: 'opacity',
            }}
            aria-hidden={!isIncoming}
          >
            {/* Both layers inactive mid-crossfade — shortcuts shouldn't dispatch
                during the animation; the settled layer owns them once idle. */}
            {renderLayer(layerExpanded, false)}
          </div>
        )
      })}
    </div>
  )
}
