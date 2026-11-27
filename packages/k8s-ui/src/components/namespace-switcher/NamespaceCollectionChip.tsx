import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { CircleDashed } from 'lucide-react'
import { clsx } from 'clsx'
import { Tooltip } from '../ui/Tooltip'
import { useDisclosure } from '../ui/Collapse'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { useKeepInViewport } from '../../hooks/useKeepInViewport'
import { TRANSITION_MENU, overlayExitMs, overlayTransitionStyle } from '../../utils/animation'
import type { NamespaceCollection } from '../../utils/namespace-collection'
import { COLLECTION_NOTE, NamespaceCollectionDetails } from './NamespaceCollectionDetails'

/** A labelled button rather than a hover tooltip, so touch and keyboard users can reach it. */
export function NamespaceCollectionChip({
  collection,
  variant,
}: {
  collection: NamespaceCollection
  variant: 'chip' | 'segment'
}) {
  const [open, setOpen] = useState(false)
  const { shouldRender, isOpen } = useAnimatedUnmount(open, overlayExitMs('menu'))
  const rootRef = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  useKeepInViewport(panelRef, shouldRender)
  const disclosure = useDisclosure(open)

  // useKeepInViewport only moves the panel sideways, so a long list scrolls inside the room below the button.
  const [roomBelow, setRoomBelow] = useState<number>()
  useLayoutEffect(() => {
    if (!open) return
    const fit = () => setRoomBelow(window.innerHeight - (buttonRef.current?.getBoundingClientRect().bottom ?? 0) - 12)
    fit()
    window.addEventListener('resize', fit)
    return () => window.removeEventListener('resize', fit)
  }, [open])

  // Listeners exist only while open, so a closed chip never swallows an Escape meant for something else.
  useEffect(() => {
    if (!open) return
    const inside = (target: EventTarget | null) => rootRef.current?.contains(target as Node) ?? false
    const onPointerDown = (e: Event) => {
      if (!inside(e.target)) setOpen(false)
    }
    const onFocusIn = (e: FocusEvent) => {
      if (!inside(e.target)) setOpen(false)
    }
    // Consumed so the same Escape doesn't also close whatever is open behind.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.preventDefault()
      e.stopPropagation()
      setOpen(false)
      if (inside(document.activeElement)) buttonRef.current?.focus()
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('focusin', onFocusIn)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('focusin', onFocusIn)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  const segment = variant === 'segment'

  return (
    <div ref={rootRef} className={clsx('relative', segment ? 'flex items-stretch' : 'inline-flex')}>
      {/* preserveWrapperWhenDisabled: remounting the button would drop keyboard focus. */}
      <Tooltip content={COLLECTION_NOTE} delay={500} position="bottom" disabled={open} preserveWrapperWhenDisabled>
        <button
          ref={buttonRef}
          type="button"
          {...disclosure.buttonProps}
          onClick={() => setOpen(v => !v)}
          aria-label="Partial: where Radar collects some kinds"
          className={clsx(
            'flex items-center gap-1 text-theme-text-secondary transition-colors hover:text-theme-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent',
            segment
              ? 'h-full rounded-r-[7px] px-2.5 text-[13px] hover:bg-theme-hover focus-visible:ring-inset'
              : 'rounded border border-theme-border bg-theme-elevated px-1.5 py-0.5 text-[11px] hover:bg-theme-hover any-pointer-coarse:py-1.5',
            open && 'text-theme-text-primary',
          )}
        >
          <CircleDashed className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>Partial</span>
        </button>
      </Tooltip>

      {shouldRender && (
        <div
          ref={panelRef}
          id={disclosure.panelId}
          role="group"
          aria-label="Where Radar collects some kinds"
          tabIndex={-1}
          inert={!open}
          className={clsx(
            'absolute left-0 top-full z-50 mt-1 w-72 max-w-[calc(100vw-1rem)] origin-top-left overflow-y-auto overscroll-contain rounded-lg border border-theme-border bg-theme-surface p-3 text-xs shadow-theme-lg focus:outline-none',
            TRANSITION_MENU,
            isOpen ? 'opacity-100 scale-100' : 'opacity-0 scale-[0.97]',
            !open && 'pointer-events-none',
          )}
          style={{ ...overlayTransitionStyle(isOpen, 'menu'), maxHeight: roomBelow }}
        >
          <NamespaceCollectionDetails collection={collection} />
        </div>
      )}
    </div>
  )
}
