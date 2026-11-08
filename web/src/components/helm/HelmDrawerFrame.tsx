import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { useDockReservedHeight } from '@skyhook-io/k8s-ui'
import { TRANSITION_DRAWER } from '../../utils/animation'

const MIN_WIDTH = 500
const MAX_WIDTH_PERCENT = 0.8
const DEFAULT_WIDTH = 1000
const HEADER_HEIGHT = 49

interface HelmDrawerFrameProps {
  onClose: () => void
  /** Controls slide-in/out animation (driven by useAnimatedUnmount) */
  isOpen: boolean
  /** Right inset in px so the drawer sits beside a docked side panel (AI), not under it. */
  rightInset: number
  children: ReactNode
}

/** The Helm drawer's position, width and Esc handling, kept across a change of content. */
export function HelmDrawerFrame({ onClose, isOpen, rightInset, children }: HelmDrawerFrameProps) {
  const [drawerWidth, setDrawerWidth] = useState(DEFAULT_WIDTH)
  const [viewportWidth, setViewportWidth] = useState(() => (typeof window === 'undefined' ? DEFAULT_WIDTH : window.innerWidth))
  const [isResizing, setIsResizing] = useState(false)
  const resizeStartX = useRef(0)
  const resizeStartWidth = useRef(DEFAULT_WIDTH)
  const dockInset = useDockReservedHeight()

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  useEffect(() => {
    const handleWindowResize = () => setViewportWidth(window.innerWidth)
    handleWindowResize()
    window.addEventListener('resize', handleWindowResize)
    return () => window.removeEventListener('resize', handleWindowResize)
  }, [])

  const minDrawerWidth = Math.min(MIN_WIDTH, viewportWidth)
  const maxDrawerWidth = viewportWidth < MIN_WIDTH ? viewportWidth : Math.max(MIN_WIDTH, viewportWidth * MAX_WIDTH_PERCENT)
  const renderedDrawerWidth = Math.max(minDrawerWidth, Math.min(drawerWidth, maxDrawerWidth))

  const handleResizeStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    setIsResizing(true)
    resizeStartX.current = e.clientX
    resizeStartWidth.current = renderedDrawerWidth
  }, [renderedDrawerWidth])

  useEffect(() => {
    if (!isResizing) return

    document.body.style.cursor = 'ew-resize'
    document.body.style.userSelect = 'none'

    const handleMouseMove = (e: MouseEvent) => {
      const deltaX = resizeStartX.current - e.clientX
      const newWidth = resizeStartWidth.current + deltaX
      const viewport = window.innerWidth
      const minWidth = Math.min(MIN_WIDTH, viewport)
      const maxWidth = viewport < MIN_WIDTH ? viewport : Math.max(MIN_WIDTH, viewport * MAX_WIDTH_PERCENT)
      setDrawerWidth(Math.max(minWidth, Math.min(newWidth, maxWidth)))
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

  return (
    <div
      className={clsx(
        'fixed right-0 bg-theme-surface border-l border-theme-border flex flex-col shadow-drawer z-40',
        TRANSITION_DRAWER,
        isOpen ? 'translate-x-0 opacity-100' : 'translate-x-full opacity-0'
      )}
      style={{ width: renderedDrawerWidth, top: HEADER_HEIGHT, right: rightInset, height: `calc(100vh - ${HEADER_HEIGHT}px - ${dockInset}px)` }}
    >
      {/* Resize handle */}
      <div
        onMouseDown={handleResizeStart}
        className={clsx(
          'absolute left-0 top-0 bottom-0 w-2 cursor-ew-resize z-10 hover:bg-blue-500/50 transition-colors',
          'hidden sm:block',
          isResizing && 'bg-blue-500/50'
        )}
      />
      {children}
    </div>
  )
}
