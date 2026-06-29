import { useState, useCallback, useEffect, useLayoutEffect, useReducer, useRef, type ReactNode } from 'react'
import {
  DURATION_DRAWER_MORPH,
  EASE_DRAWER_MORPH,
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
  /** Called when user clicks expand button. `opts.yaml` is true when expanding
   *  from the drawer's YAML view, so the host can open the full view on YAML. */
  onExpand?: (resource: SelectedResource, opts?: { yaml?: boolean }) => void
  /** Whether the expanded view can collapse back to a side drawer. False on
   *  mobile (no room for a drawer) — the collapse-to-drawer control is hidden
   *  there and the host's onCollapse should close instead. Default true. */
  canCollapseToDrawer?: boolean
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
    onExpand?: (opts?: { yaml?: boolean }) => void
    /** Signal (hover/press the expand control) that expand is likely — pre-mounts
     *  the heavy fullscreen layer invisibly so the click starts the morph instantly. */
    onExpandIntent?: () => void
    /** Intent withdrawn (pointer left the expand control) — discard the pre-mount. */
    onCancelExpandIntent?: () => void
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

export function ResourceDetailDrawer({ resource, onClose, onNavigate, initialTab, isOpen = true, expanded, onCollapse, onExpand, canCollapseToDrawer = true, onNavigateToResource, headerHeight: headerHeightProp, leftOffset = 0, children }: ResourceDetailDrawerProps) {
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

  // Pre-mount: hovering/pressing the expand control mounts the heavy fullscreen
  // layer invisibly ahead of the click, so the click reuses it (same key) and the
  // morph's first frames aren't competing with the layer's mount cost.
  const [prewarmExpand, setPrewarmExpand] = useState(false)

  const measureFullWidth = useCallback(() => {
    const el = containerRef.current
    const parent = el?.offsetParent as HTMLElement | null
    const measured = parent ? parent.clientWidth - leftOffset : el?.clientWidth
    if (measured && measured > 0) setFullWidthPx(measured)
  }, [leftOffset])

  const handleExpandIntent = useCallback(() => {
    if (expanded || transitioning) return
    measureFullWidth()
    setPrewarmExpand(true)
  }, [expanded, transitioning, measureFullWidth])

  const handleCancelExpandIntent = useCallback(() => {
    if (transitioning || expanded) return
    setPrewarmExpand(false)
  }, [transitioning, expanded])

  useLayoutEffect(() => {
    if (settledExpanded.current === !!expanded) return
    if (prefersReducedMotion) {
      settledExpanded.current = !!expanded
      setPrewarmExpand(false)
      forceSettle()
      return
    }
    // Pin the full-screen layer to the measured expanded width so its content
    // lays out at its final width from the first frame (no live reflow).
    measureFullWidth()
    // The prewarm layer (if any) becomes the real transition layer now (reused by key).
    setPrewarmExpand(false)
    // Double-rAF so the incoming layer's mount+paint lands BEFORE the motion starts
    // (else it drops frames in the first third). Prewarm keeps the heavy mount off
    // the click path; these two frames are then cheap (the layer's already there).
    setCrossfadeArmed(false)
    let raf2 = 0
    let timer = 0
    const arm = () => {
      setCrossfadeArmed(true)
      timer = window.setTimeout(() => {
        settledExpanded.current = !!expanded
        setCrossfadeArmed(false)
        forceSettle()
      }, DURATION_DRAWER_MORPH)
    }
    const raf1 = requestAnimationFrame(() => {
      raf2 = requestAnimationFrame(arm)
    })
    return () => {
      cancelAnimationFrame(raf1)
      cancelAnimationFrame(raf2)
      clearTimeout(timer)
    }
  }, [expanded, prefersReducedMotion, measureFullWidth])

  // Reset drawer width when resource kind changes
  useEffect(() => {
    const w = getDefaultWidth(resource.kind)
    setDrawerWidth(w)
    resizeStartWidth.current = w
  }, [resource.kind])

  // Drop a pending prewarm if the drawer closes or the target resource changes —
  // its hover intent is stale, and a reopened drawer shouldn't pre-mount unbidden.
  useEffect(() => {
    setPrewarmExpand(false)
  }, [isOpen, resource.kind, resource.namespace, resource.name])

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
      onExpand: onExpand ? (opts) => onExpand(resource, opts) : undefined,
      onExpandIntent: onExpand ? handleExpandIntent : undefined,
      onCancelExpandIntent: onExpand ? handleCancelExpandIntent : undefined,
      onBack: onCollapse ? () => onCollapse() : undefined,
      onNavigateToResource: handleNavigate,
      // Hidden on mobile (no drawer to collapse to) — the host routes the
      // back/close control through onCollapse instead.
      onCollapseToDrawer: (canCollapseToDrawer && onCollapse) ? () => onCollapse() : undefined,
    })

  // While transitioning render both layers (outgoing = settled state, incoming
  // = target). Keying by expanded-ness keeps the incoming layer's React
  // identity stable into idle, so it survives (state intact) while only the
  // outgoing layer unmounts when the window ends. While prewarming (collapsed,
  // intent signalled) also mount the expanded layer invisibly — keyed 'expanded'
  // so it's reused (not remounted) when the real expand starts.
  const showPrewarmLayer = prewarmExpand && isOpen && !transitioning && !expanded
  const layerExpandedValues = transitioning ? [true, false] : showPrewarmLayer ? [false, true] : [!!expanded]

  return (
    <div
      ref={containerRef}
      className={clsx(
        'absolute right-0 bg-theme-surface border-l border-theme-border flex flex-col z-40',
        // Clip the wider layer only while morphing; keep overflow visible at idle
        // so drawer popovers/tooltips aren't clipped.
        transitioning && 'overflow-hidden',
        // No drawer shadow in fullscreen — it's a full page, not a floating panel.
        !expanded && 'shadow-drawer',
        isOpen
          ? 'translate-x-0 opacity-100'
          : 'translate-x-full opacity-0',
        expanded && '!border-l-0',
      )}
      style={{
        width: containerWidth,
        top: headerHeight,
        height: `calc(100% - ${headerHeight}px - ${dockInset}px)`,
        // Inline so the morph duration/easing is controlled precisely (and stays
        // in lockstep with the crossfade + JS window). Width only animates when
        // not resizing / reduced-motion.
        transition: `translate ${DURATION_DRAWER_MORPH}ms ${EASE_DRAWER_MORPH}, opacity ${DURATION_DRAWER_MORPH}ms ${EASE_DRAWER_MORPH}${animateWidth ? `, width ${DURATION_DRAWER_MORPH}ms ${EASE_DRAWER_MORPH}` : ''}`,
        willChange: 'transform, width',
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
          // Prewarm: the expanded layer mounted invisibly ahead of the click.
          // Pinned at full width + inert + opacity 0 so its mount cost is paid now,
          // off-screen, and it's reused (same key) when the morph actually starts.
          if (showPrewarmLayer && layerExpanded) {
            return (
              <div
                key="expanded"
                aria-hidden
                inert
                className="absolute top-0 bottom-0 right-0 overflow-hidden pointer-events-none opacity-0"
                style={{ width: fullWidthPx ?? `calc(100% - ${leftOffset}px)` }}
              >
                {renderLayer(true, false)}
              </div>
            )
          }
          // Idle (or the visible collapsed layer during prewarm): fills the container.
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
              transition: `opacity ${DURATION_DRAWER_MORPH}ms ${EASE_DRAWER_MORPH}`,
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
