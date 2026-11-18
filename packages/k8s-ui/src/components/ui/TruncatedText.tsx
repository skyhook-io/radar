import { useEffect, useRef, useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { Tooltip } from './Tooltip'

/**
 * Single-line text that ends in "…" when it does not fit, and shows its full
 * text in a tooltip only while it is actually cut.
 *
 * A column's width can change with the window, a drag or the data, so whether
 * the text fits is read from the element's own overflow rather than computed.
 *
 * `tooltip` is for a tooltip that says more than the visible text (every image
 * when the cell shows the first, the namespace beside an object's name). It is
 * shown whether or not the text is cut; without it, the tooltip is the text.
 *
 * The Tooltip wrapper stays mounted whether or not the text is cut: swapping it
 * in on overflow would remount the measured span, stranding the ResizeObserver
 * on the detached node so the state could never flip back. It also needs
 * min-w-0 so it can shrink inside a flex row.
 */
export function TruncatedText({ text, tooltip, className, children }: {
  text: string
  tooltip?: ReactNode
  className?: string
  /** Rendered in place of `text` (e.g. a link); `text` is still the tooltip. */
  children?: ReactNode
}) {
  const ref = useRef<HTMLSpanElement>(null)
  const [truncated, setTruncated] = useState(false)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    let live = true
    const check = () => { if (live) setTruncated(el.scrollWidth > el.clientWidth + 0.5) }
    check()
    // The web font can arrive after mount and widen the text without resizing
    // the span, which the ResizeObserver would not see.
    void (document as Document & { fonts?: FontFaceSet }).fonts?.ready?.then(check)
    const ro = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(check)
    ro?.observe(el)
    return () => { live = false; ro?.disconnect() }
  }, [text])
  return (
    <Tooltip
      content={tooltip ?? text}
      disabled={!tooltip && !truncated}
      preserveWrapperWhenDisabled
      wrapperClassName="min-w-0 max-w-full"
    >
      <span ref={ref} className={clsx('truncate block', className)}>{children ?? text}</span>
    </Tooltip>
  )
}
