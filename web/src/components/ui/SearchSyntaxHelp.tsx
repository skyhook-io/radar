import { useState, useRef, useEffect } from 'react'
import { createPortal } from 'react-dom'
import { HelpCircle } from 'lucide-react'
import { clsx } from 'clsx'

// The query operators recognized by the search engine (internal/search/parse.go).
const OPERATORS: { syntax: string; desc: string }[] = [
  { syntax: 'ns:prod', desc: 'filter by namespace' },
  { syntax: 'kind:Deployment', desc: 'filter by resource kind' },
  { syntax: 'label:app=api', desc: 'filter by label' },
  { syntax: 'image:nginx', desc: 'match container image' },
  { syntax: 'redis cache', desc: 'multiple terms — all must match' },
]

// A small "?" affordance that reveals the search query syntax. Self-contained:
// owns its popover, portaled to <body> and anchored to the button so the
// header's stacking context can't trap it.
export function SearchSyntaxHelp() {
  const [open, setOpen] = useState(false)
  const btnRef = useRef<HTMLButtonElement>(null)
  const popRef = useRef<HTMLDivElement>(null)
  const [anchor, setAnchor] = useState<{ right: number; top: number } | null>(null)

  useEffect(() => {
    if (!open) { setAnchor(null); return }
    const update = () => {
      const el = btnRef.current
      if (el) { const r = el.getBoundingClientRect(); setAnchor({ right: window.innerWidth - r.right, top: r.bottom + 6 }) }
    }
    update()
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node
      if (!btnRef.current?.contains(t) && !popRef.current?.contains(t)) setOpen(false)
    }
    window.addEventListener('resize', update)
    window.addEventListener('scroll', update, true)
    document.addEventListener('mousedown', onDown)
    return () => {
      window.removeEventListener('resize', update)
      window.removeEventListener('scroll', update, true)
      document.removeEventListener('mousedown', onDown)
    }
  }, [open])

  return (
    <>
      <button
        ref={btnRef}
        type="button"
        tabIndex={-1}
        aria-label="Search syntax help"
        onMouseDown={(e) => { e.preventDefault(); e.stopPropagation(); setOpen((o) => !o) }}
        className={clsx('shrink-0 rounded p-0.5 transition-colors', open ? 'text-theme-text-primary' : 'text-theme-text-tertiary hover:text-theme-text-secondary')}
      >
        <HelpCircle className="w-3.5 h-3.5" />
      </button>
      {open && anchor && createPortal(
        <div
          ref={popRef}
          onMouseDown={(e) => e.stopPropagation()}
          style={{ position: 'fixed', top: anchor.top, right: anchor.right, width: 280 }}
          className="z-[130] dialog shadow-theme-lg p-3"
        >
          <div className="text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary mb-2">Search syntax</div>
          <div className="space-y-1.5">
            {OPERATORS.map((op) => (
              <div key={op.syntax} className="flex items-baseline gap-2">
                <code className="shrink-0 rounded bg-theme-elevated border border-theme-border-light px-1.5 py-0.5 text-xs text-theme-text-primary">{op.syntax}</code>
                <span className="text-xs text-theme-text-secondary">{op.desc}</span>
              </div>
            ))}
          </div>
          <div className="mt-2.5 pt-2 border-t border-theme-border text-[11px] text-theme-text-tertiary">
            Type <code className="text-theme-text-secondary">ns:</code> or <code className="text-theme-text-secondary">kind:</code> for value suggestions.
          </div>
        </div>,
        document.body,
      )}
    </>
  )
}
