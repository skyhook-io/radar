import { useEffect, useRef, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react'
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

  // Bubble phase lets nested editors and menus consume Escape before the dialog closes.
  const handleDialogKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'Escape') return

    e.stopPropagation()
    e.preventDefault()

    if (closable) {
      onClose()
    }
  }

  useEffect(() => {
    if (!open) return

    const handleDocumentKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      const modalDialogs = Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"][aria-modal="true"]'))
      const topDialog = modalDialogs[modalDialogs.length - 1]
      if (topDialog !== dialogRef.current) return

      if (dialogRef.current?.contains(e.target as Node)) return

      e.stopPropagation()
      e.preventDefault()

      if (closable) {
        onClose()
      }
    }

    document.addEventListener('keydown', handleDocumentKeyDown, true)
    return () => document.removeEventListener('keydown', handleDocumentKeyDown, true)
  }, [open, closable, onClose])

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
        onKeyDown={handleDialogKeyDown}
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
