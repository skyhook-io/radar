import { useEffect, useId, useState, type ReactNode, type TransitionEvent } from 'react'
import { ChevronRight } from 'lucide-react'
import { clsx } from 'clsx'
import { DURATION_DISCLOSURE, TRANSITION_CHEVRON, TRANSITION_DISCLOSURE, prefersReducedMotion } from '../../utils/animation'

// Animated show/hide using the grid-template-rows 0fr→1fr technique — the
// app-wide standard for expand/collapse (drawer sections, checks, audit,
// resources sidebar, issue rows, and every disclosure in Radar Cloud). The
// inner overflow-hidden wrapper is load-bearing: it clips the content while
// the grid row animates between 0 and its natural height, so nothing spills
// before the row is fully open.
//
// Timing lives in utils/animation (DURATION_DISCLOSURE, one curve). The caret
// (CollapseChevron) runs the same clock, so an interrupted toggle reverses
// panel and caret together.
//
// Mount policy — pick per site, it is not cosmetic:
//   default        content stays mounted while closed, clipped to zero height
//                  and `inert` so keyboard/screen-reader users can't land on
//                  invisible rows. Right for static or cheap content.
//   mountLazily    defers the first render until the first open, then keeps
//                  it. For many instances with heavy collapsed content
//                  (hundreds of rows each with a drift panel).
//   unmountOnExit  unmounts the children once the close transition has
//                  finished (transitionend on this wrapper, with a timer
//                  fallback so disposal is guaranteed under reduced motion,
//                  for empty content, or if the event never fires). For
//                  subtrees that poll, subscribe, or should reset on close.
//                  Note: work stops when the subtree unmounts, i.e. after the
//                  exit; a query that must stop at logical close should also
//                  take `enabled: open`.
export function Collapse({
  open,
  children,
  className,
  mountLazily = false,
  unmountOnExit = false,
  id,
}: {
  open: boolean
  children: ReactNode
  className?: string
  mountLazily?: boolean
  unmountOnExit?: boolean
  /** Panel id for the header's aria-controls (see useDisclosure). */
  id?: string
}) {
  // Latch: once opened, stay mounted. Conditional setState-in-render is the
  // supported pattern for deriving state from props without an extra commit.
  const [hasOpened, setHasOpened] = useState(open)
  if (mountLazily && open && !hasOpened) setHasOpened(true)

  // unmountOnExit: keep rendering through the close transition, then stop.
  // `exiting` must flip in the SAME render that sees `open` go false — doing
  // it in an effect would commit one frame with the children already gone
  // (render = open || exiting, both false) before the effect put them back.
  // Same setState-in-render pattern as the latch above.
  const [exiting, setExiting] = useState(false)
  const [prevOpen, setPrevOpen] = useState(open)
  if (open !== prevOpen) {
    setPrevOpen(open)
    // Reopen before disposal cancels the pending unmount; under reduced motion
    // the stylesheet has zeroed the transition, so dispose at logical close.
    setExiting(!open && unmountOnExit && !prefersReducedMotion())
  }
  // Guarantee disposal even if transitionend never arrives (empty content
  // transitions instantly; a cancelled transition fires no end). Any change to
  // `exiting` (reopen, transitionend) clears the pending timer via cleanup.
  useEffect(() => {
    if (!exiting) return
    const t = window.setTimeout(() => setExiting(false), DURATION_DISCLOSURE + 50)
    return () => window.clearTimeout(t)
  }, [exiting])

  const onTransitionEnd = (event: TransitionEvent<HTMLDivElement>) => {
    if (!exiting) return
    // Only this wrapper's own row transition — descendants' transitions
    // bubble here too.
    if (event.target !== event.currentTarget) return
    if (event.propertyName !== 'grid-template-rows') return
    setExiting(false)
  }

  const render = unmountOnExit ? open || exiting : !mountLazily || hasOpened
  return (
    <div
      id={id}
      className={clsx('grid', TRANSITION_DISCLOSURE, className)}
      style={{ gridTemplateRows: open ? '1fr' : '0fr' }}
      onTransitionEnd={onTransitionEnd}
    >
      {/* `relative` is load-bearing. Collapsed content is still laid out at full
          height and only clipped by `overflow-hidden` — but an absolutely-positioned
          descendant (Tailwind's `sr-only` is position:absolute) resolves its
          containing block ABOVE this wrapper, so the clip doesn't apply to it. It
          then sits at its static position, hundreds of px below a collapsed section,
          and extends the enclosing scroll container's scrollable overflow: the drawer
          scrolls past its own content into blank space. Anchoring here puts those
          boxes back inside the clip, so a collapsed section contributes nothing. */}
      <div className="relative overflow-hidden" inert={!open || undefined}>
        {render ? children : null}
      </div>
    </div>
  )
}

// CollapseChevron is the disclosure caret that pairs with <Collapse>: a single
// ChevronRight that rotates 90° when open, rather than swapping between two
// icons. Same duration and curve as the panel, so every collapsible surface
// animates the same way and an interrupted toggle keeps caret and panel in
// step. Menu triggers are a different thing — they keep ChevronDown/Up
// flipping toward the menu.
//
// The caret is tertiary grey by default. Inside a tinted header (an amber
// warning, a red error banner, a button whose hover recolors its text) pass
// `inheritColor` so it takes currentColor from the header instead — a color
// utility in `className` would race the default in the cascade.
export function CollapseChevron({
  open,
  className,
  inheritColor = false,
}: {
  open: boolean
  className?: string
  inheritColor?: boolean
}) {
  return (
    <ChevronRight
      aria-hidden="true"
      className={clsx('shrink-0', !inheritColor && 'text-theme-text-tertiary', TRANSITION_CHEVRON, open && 'rotate-90', className)}
    />
  )
}

// useDisclosure wires the header ↔ panel relationship: the header gets
// aria-expanded + aria-controls, the panel gets a stable id. Works with any
// header markup (a plain button, or a compound row with its own actions), so
// it does not force a <button> wrapper around things that already contain
// buttons.
//
//   const d = useDisclosure(open)
//   <button {...d.buttonProps} onClick={toggle}>…<CollapseChevron open={open} /></button>
//   <Collapse open={open} id={d.panelId}>…</Collapse>
// Rows mapped inline can't each call useDisclosure; they build ids from one
// useId() prefix plus a row key. The key is display data (a category name, a
// cluster id) and may contain whitespace, which would split `aria-controls`
// into two references — so it is percent-encoded here (injective, so two
// distinct keys can't land on one id).
export function disclosurePanelId(base: string, key: string | number): string {
  return `${base}-${encodeURIComponent(String(key))}`
}

export function useDisclosure(open: boolean, explicitId?: string) {
  const generated = useId()
  const panelId = explicitId ?? `disclosure-${generated}`
  return {
    panelId,
    buttonProps: { 'aria-expanded': open, 'aria-controls': panelId } as const,
  }
}
