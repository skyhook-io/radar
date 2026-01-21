import { ReactNode, useState, useRef, useEffect, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { clsx } from 'clsx'

interface TooltipProps {
  content: ReactNode
  children: ReactNode
  /** Delay before showing tooltip in ms (default: 300) */
  delay?: number
  /** Position of tooltip (default: 'top') */
  position?: 'top' | 'bottom' | 'left' | 'right'
  /** Additional class for the tooltip content */
  className?: string
  /** Whether tooltip is disabled */
  disabled?: boolean
}

export function Tooltip({
  content,
  children,
  delay = 300,
  position = 'top',
  className,
  disabled = false,
}: TooltipProps) {
  const [isVisible, setIsVisible] = useState(false)
  const [coords, setCoords] = useState({ top: 0, left: 0 })
  const triggerRef = useRef<HTMLSpanElement>(null)
  const tooltipRef = useRef<HTMLSpanElement>(null)
  const timeoutRef = useRef<number | null>(null)

  const updatePosition = useCallback(() => {
    if (!triggerRef.current) return

    const rect = triggerRef.current.getBoundingClientRect()
    const tooltipRect = tooltipRef.current?.getBoundingClientRect()
    const tooltipWidth = tooltipRect?.width || 0
    const tooltipHeight = tooltipRect?.height || 0

    let top = 0
    let left = 0

    switch (position) {
      case 'top':
        top = rect.top - tooltipHeight - 6
        left = rect.left + rect.width / 2 - tooltipWidth / 2
        break
      case 'bottom':
        top = rect.bottom + 6
        left = rect.left + rect.width / 2 - tooltipWidth / 2
        break
      case 'left':
        top = rect.top + rect.height / 2 - tooltipHeight / 2
        left = rect.left - tooltipWidth - 6
        break
      case 'right':
        top = rect.top + rect.height / 2 - tooltipHeight / 2
        left = rect.right + 6
        break
    }

    // Keep tooltip within viewport
    const padding = 8
    if (left < padding) left = padding
    if (left + tooltipWidth > window.innerWidth - padding) {
      left = window.innerWidth - tooltipWidth - padding
    }
    if (top < padding) top = rect.bottom + 6 // flip to bottom
    if (top + tooltipHeight > window.innerHeight - padding) {
      top = rect.top - tooltipHeight - 6 // flip to top
    }

    setCoords({ top, left })
  }, [position])

  const showTooltip = () => {
    if (disabled || !content) return
    timeoutRef.current = window.setTimeout(() => {
      setIsVisible(true)
    }, delay)
  }

  const hideTooltip = () => {
    if (timeoutRef.current) {
      clearTimeout(timeoutRef.current)
      timeoutRef.current = null
    }
    setIsVisible(false)
  }

  useEffect(() => {
    if (isVisible) {
      // Small delay to let the tooltip render before measuring
      requestAnimationFrame(updatePosition)
    }
  }, [isVisible, updatePosition])

  useEffect(() => {
    return () => {
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current)
      }
    }
  }, [])

  if (disabled || !content) {
    return <>{children}</>
  }

  return (
    <>
      <span
        ref={triggerRef}
        className="inline-flex max-w-full"
        onMouseEnter={showTooltip}
        onMouseLeave={hideTooltip}
        onFocus={showTooltip}
        onBlur={hideTooltip}
      >
        {children}
      </span>
      {isVisible &&
        createPortal(
          <span
            ref={tooltipRef}
            className={clsx(
              'fixed z-[9999] px-2 py-1 text-xs text-theme-text-primary bg-theme-base rounded shadow-lg',
              'whitespace-nowrap pointer-events-none',
              className
            )}
            style={{ top: coords.top, left: coords.left }}
            role="tooltip"
          >
            {content}
          </span>,
          document.body
        )}
    </>
  )
}

/** Simple wrapper that adds tooltip to any element - use for quick migrations from title="" */
export function WithTooltip({
  tip,
  children,
  delay = 300,
}: {
  tip: string | undefined | null
  children: ReactNode
  delay?: number
}) {
  if (!tip) return <>{children}</>
  return (
    <Tooltip content={tip} delay={delay}>
      {children}
    </Tooltip>
  )
}
