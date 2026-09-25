import {
  useEffect,
  useRef,
  type FocusEvent as ReactFocusEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from 'react'
import { createPortal } from 'react-dom'
import { clsx } from 'clsx'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_BACKDROP, TRANSITION_PANEL, overlayExitMs, overlayTransitionStyle } from '../../utils/animation'

interface DialogPortalProps {
  open: boolean
  onClose: () => void
  children: ReactNode
  /** Extra classes on the panel container (width, max-height, etc.) */
  className?: string
  /** Prevent closing via Escape / backdrop click (e.g. during async operation) */
  closable?: boolean
  /** Id of the element that names the dialog (usually its title). Preferred over `ariaLabel`. */
  ariaLabelledBy?: string
  /** Accessible name when there is no visible title to reference. */
  ariaLabel?: string
}

const FOCUS_GUARD = 'fixed top-0 left-0 w-px h-0 overflow-hidden outline-none'

const TABBABLE_SELECTOR = [
  'a[href]',
  'area[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  'iframe',
  '[contenteditable]:not([contenteditable="false"])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

// A CSS-hidden element (display:none, visibility:hidden) can't take focus, so
// treating it as an edge would let Tab slip past the real last element.
function isVisible(el: HTMLElement): boolean {
  return typeof el.checkVisibility === 'function' ? el.checkVisibility({ visibilityProperty: true }) : true
}

function tabbableElements(panel: HTMLElement): HTMLElement[] {
  return Array.from(panel.querySelectorAll<HTMLElement>(TABBABLE_SELECTOR)).filter(
    el => el.tabIndex >= 0 && !el.closest('[inert],[hidden]') && isVisible(el),
  )
}

/**
 * Minimal dialog primitive — handles portal, backdrop, escape, focus, animation.
 * Renders children inside a centered panel portaled to document.body, so it works
 * correctly even inside CSS-transformed containers (drawers, slide panels).
 *
 * Focus moves into the panel on open (unless a child already took it, e.g. via
 * `autoFocus`), Tab wraps at the panel's edges, and focus returns to the element
 * that opened the dialog at logical close.
 *
 * Usage:
 *   <DialogPortal open={showDialog} onClose={() => setShowDialog(false)} className="w-80" ariaLabelledBy={titleId}>
 *     <h3 id={titleId}>Title</h3>
 *     <p>Content</p>
 *   </DialogPortal>
 */
export function DialogPortal({
  open,
  onClose,
  children,
  className,
  closable = true,
  ariaLabelledBy,
  ariaLabel,
}: DialogPortalProps) {
  const dialogRef = useRef<HTMLDivElement>(null)
  // `el` is null when focus entered from nowhere restorable (body, or an
  // element outside the panel that has since been removed).
  const openerRef = useRef<{ el: HTMLElement | null } | null>(null)
  const pendingRestoreRef = useRef(false)
  // The unmount window is the exit duration: shorter than the entrance, and
  // exactly what the panel transition below runs — a mismatch here cut the
  // close at two-thirds (200ms window under a 300ms panel).
  const { shouldRender, isOpen } = useAnimatedUnmount(open, overlayExitMs('dialog'))

  // Bubble phase lets nested editors and menus consume Escape before the dialog closes.
  const handleDialogKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'Escape') return

    e.stopPropagation()
    e.preventDefault()

    if (closable) {
      onClose()
    }
  }

  // Guards on either side of the panel catch focus leaving it, so inside the
  // panel the browser's own Tab order applies — scroll containers, radio
  // groups, editors that claim Tab — and only the wrap is decided here.
  const focusEdge = (toLast: boolean) => {
    const panel = dialogRef.current
    if (!panel) return
    const items = tabbableElements(panel)
    const target = (toLast ? items[items.length - 1] : items[0]) ?? panel
    target.focus()
  }

  // Leaving past one edge wraps to the other; arriving from outside the panel
  // enters at the near edge.
  const handleGuardFocus = (edge: 'start' | 'end') => (e: ReactFocusEvent<HTMLSpanElement>) => {
    const panel = dialogRef.current
    const fromInside = e.relatedTarget instanceof Node && !!panel?.contains(e.relatedTarget)
    focusEdge(edge === 'start' ? fromInside : !fromInside)
  }

  // Focus drops to body when the focused control is disabled or removed (a
  // confirm button entering its loading state); the next Tab would then land
  // behind the modal. Focus elsewhere (a menu portaled out of the panel) is
  // left alone.
  const recoverStrayTab = (e: KeyboardEvent) => {
    const panel = dialogRef.current
    const active = document.activeElement
    if (!panel || (active && active !== document.body)) return
    e.preventDefault()
    focusEdge(e.shiftKey)
  }

  // relatedTarget on the first focus into the panel is the opener. Reading
  // document.activeElement in an effect would miss it when a child `autoFocus`
  // has already moved focus during commit — which also happens before
  // dialogRef is attached, hence currentTarget.
  const handlePanelFocus = (e: ReactFocusEvent<HTMLDivElement>) => {
    const panel = e.currentTarget
    if (!open || openerRef.current || !panel.contains(e.target as Node)) return
    const from = e.relatedTarget
    openerRef.current = { el: from instanceof HTMLElement && !panel.contains(from) ? from : null }
  }

  useEffect(() => {
    if (!open) return

    const handleDocumentKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' && e.key !== 'Tab') return
      // A dialog still animating out is inert and no longer the one on top.
      const modalDialogs = Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"][aria-modal="true"]'))
        .filter(d => !d.closest('[inert]'))
      const topDialog = modalDialogs[modalDialogs.length - 1]
      if (topDialog !== dialogRef.current) return

      if (e.key === 'Tab') {
        recoverStrayTab(e)
        return
      }

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

  // The panel mounts a render after `open` flips (useAnimatedUnmount sets
  // shouldRender in an effect), so this must also wait on shouldRender.
  useEffect(() => {
    const panel = dialogRef.current
    if (!open || !shouldRender || !panel) return
    if (!panel.contains(document.activeElement)) panel.focus()
  }, [open, shouldRender])

  // Restore at logical close (open → false, or unmount while open), not after
  // the exit. The microtask lets StrictMode's simulated unmount/remount cancel
  // the restore; otherwise dev builds would pull focus out of an autoFocus child.
  useEffect(() => {
    if (!open) return
    pendingRestoreRef.current = false
    return () => {
      pendingRestoreRef.current = true
      queueMicrotask(() => {
        if (!pendingRestoreRef.current) return
        pendingRestoreRef.current = false
        const opener = openerRef.current?.el
        openerRef.current = null
        if (!opener?.isConnected) return
        const active = document.activeElement
        const panel = dialogRef.current
        // Don't steal focus from wherever the close action deliberately sent it.
        const focusStillOurs = !active || active === document.body || !!panel?.contains(active)
        if (focusStillOurs) opener.focus({ preventScroll: true })
      })
    }
  }, [open])

  if (!shouldRender) return null

  return createPortal(
    <div
      className={clsx('fixed inset-0 z-50 flex items-center justify-center', !open && 'pointer-events-none')}
      inert={!open}
    >
      <div
        className={clsx(
          'absolute inset-0 bg-black/60 backdrop-blur-sm',
          TRANSITION_BACKDROP,
          isOpen ? 'opacity-100' : 'opacity-0',
        )}
        style={overlayTransitionStyle(isOpen, 'dialog')}
        onClick={closable ? onClose : undefined}
      />
      <span tabIndex={0} aria-hidden data-focus-guard onFocus={handleGuardFocus('start')} className={FOCUS_GUARD} />
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={ariaLabelledBy}
        aria-label={ariaLabelledBy ? undefined : ariaLabel}
        tabIndex={-1}
        onKeyDown={handleDialogKeyDown}
        onFocus={handlePanelFocus}
        className={clsx(
          'relative bg-theme-surface border border-theme-border rounded-lg shadow-2xl mx-4 outline-none',
          TRANSITION_PANEL,
          isOpen ? 'opacity-100 scale-100' : 'opacity-0 scale-95',
          className,
        )}
        style={overlayTransitionStyle(isOpen, 'dialog')}
      >
        {children}
      </div>
      <span tabIndex={0} aria-hidden data-focus-guard onFocus={handleGuardFocus('end')} className={FOCUS_GUARD} />
    </div>,
    document.body,
  )
}
