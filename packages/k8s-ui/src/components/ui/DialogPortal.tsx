import { useEffect, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { clsx } from 'clsx'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_BACKDROP, TRANSITION_PANEL } from '../../utils/animation'

interface DialogPortalProps {
  open: boolean
  onClose: () => void
  children: ReactNode
  /** Extra classes on the panel container (width, max-height, etc.) */
  className?: string
  /** Prevent closing via Escape / backdrop click (e.g. during async operation) */
  closable?: boolean
}

/**
 * Minimal dialog primitive — handles portal, backdrop, escape, focus, animation.
 * Renders children inside a centered panel portaled to document.body, so it works
 * correctly even inside CSS-transformed containers (drawers, slide panels).
 *
 * Usage:
 *   <DialogPortal open={showDialog} onClose={() => setShowDialog(false)} className="w-80">
 *     <h3>Title</h3>
 *     <p>Content</p>
 *   </DialogPortal>
 */
export function DialogPortal({ open, onClose, children, className, closable = true }: DialogPortalProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  const { shouldRender, isOpen } = useAnimatedUnmount(open, 200)

  // Capture-phase ESC handler — stops event before it reaches document listeners (e.g. drawer shortcuts)
  useEffect(() => {
    if (!open) return
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && closable) {
        e.stopPropagation()
        e.preventDefault()
        onClose()
      }
    }
    document.addEventListener('keydown', handleKeyDown, true)
    return () => document.removeEventListener('keydown', handleKeyDown, true)
  }, [open, onClose, closable])

  // Move focus into the dialog for accessibility and tab navigation
  useEffect(() => {
    if (open && dialogRef.current) {
      dialogRef.current.focus()
    }
  }, [open])

  if (!shouldRender) return null

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div
        className={clsx(
          'absolute inset-0 bg-black/60 backdrop-blur-sm',
          TRANSITION_BACKDROP,
          isOpen ? 'opacity-100' : 'opacity-0',
        )}
        onClick={closable ? onClose : undefined}
      />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        tabIndex={-1}
        className={clsx(
          'relative bg-theme-surface border border-theme-border rounded-lg shadow-2xl mx-4 outline-none',
          TRANSITION_PANEL,
          isOpen ? 'opacity-100 scale-100' : 'opacity-0 scale-95',
          className,
        )}
      >
        {children}
      </div>
    </div>,
    document.body,
  )
}
