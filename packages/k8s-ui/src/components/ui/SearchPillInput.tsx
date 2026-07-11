import { useRef, useState, useMemo, useEffect, useCallback } from 'react'
import { createPortal } from 'react-dom'
import { X } from 'lucide-react'
import { clsx } from 'clsx'
import { Input } from './Input'

// A structured search input: free-text plus removable `key:value` modifier
// PILLS (ns:, kind:, label:, …). Typing a complete modifier token and pressing
// space converts it to a pill; backspace from an empty input pops the last pill
// back to editable text. Modifiers with a known, bounded value set (ns, kind)
// get an inline autocomplete dropdown.
//
// PURE + host-agnostic: it knows nothing about search execution, results, or any
// app's data layer — the host injects `modifierOptions` and reads the structured
// query back via onChange. Shared so Radar (standalone) and Radar Cloud can both
// build their own results surface on top of the same advanced-search field.

export interface SearchModifier {
  key: string
  value: string
}

export interface SearchPillInputProps {
  /** Free-text portion (everything that isn't a committed modifier pill). */
  text: string
  /** Committed modifier pills. */
  pills: SearchModifier[]
  onChange: (next: { text: string; pills: SearchModifier[] }) => void
  /** Keys the control doesn't consume are forwarded (result nav: arrows/enter/etc). */
  onKeyDown?: (e: React.KeyboardEvent) => void
  onFocus?: () => void
  placeholder?: string
  /** Known values per CANONICAL modifier key for autocomplete (filtered locally). */
  modifierOptions?: Record<string, string[]>
  /** Alias→canonical map; defaults to the k8s search parser's set. */
  aliases?: Record<string, string>
  inputRef?: React.RefObject<HTMLInputElement | null>
  leftSlot?: React.ReactNode
  rightSlot?: React.ReactNode
  /** Applied to the input container (host owns the box chrome: height, bg, border). */
  className?: string
  /** Applied to the `<input>` itself (host owns text size: e.g. a hero variant). */
  inputClassName?: string
  'aria-label'?: string
  /** Fires when the modifier autocomplete opens/closes, so the host can suppress
      its own results dropdown while a modifier is being completed. */
  onSuggestingChange?: (suggesting: boolean) => void
}

// Matches internal/search/parse.go.
const DEFAULT_ALIASES: Record<string, string> = {
  ns: 'ns', n: 'ns', namespace: 'ns',
  kind: 'kind', k: 'kind',
  label: 'label', l: 'label',
  image: 'image', img: 'image',
  cluster: 'cluster', c: 'cluster',
}

const MAX_SUGGESTIONS = 50

function splitLastToken(s: string): { before: string; token: string } {
  const i = s.lastIndexOf(' ')
  if (i < 0) return { before: '', token: s }
  return { before: s.slice(0, i + 1), token: s.slice(i + 1) }
}

// The modifier currently being typed at the end of `text` (e.g. "ns:dev"), or
// null. `partial` is the value-so-far; `before` is the text that precedes it.
function activeModifier(
  text: string,
  aliases: Record<string, string>,
): { canon: string; partial: string; before: string } | null {
  const { before, token } = splitLastToken(text)
  const m = /^([a-zA-Z]+):(\S*)$/.exec(token)
  if (!m) return null
  const canon = aliases[m[1].toLowerCase()]
  if (!canon) return null
  return { canon, partial: m[2], before }
}

function highlightPartial(text: string, partial: string): React.ReactNode {
  if (!partial) return text
  const i = text.toLowerCase().indexOf(partial.toLowerCase())
  if (i < 0) return text
  return (
    <>
      {text.slice(0, i)}
      <span className="font-semibold text-[var(--color-brand)]">{text.slice(i, i + partial.length)}</span>
      {text.slice(i + partial.length)}
    </>
  )
}

export function SearchPillInput({
  text,
  pills,
  onChange,
  onKeyDown,
  onFocus,
  placeholder,
  modifierOptions,
  aliases = DEFAULT_ALIASES,
  inputRef: inputRefProp,
  leftSlot,
  rightSlot,
  className,
  inputClassName,
  onSuggestingChange,
  ...rest
}: SearchPillInputProps) {
  const internalRef = useRef<HTMLInputElement>(null)
  const inputRef = inputRefProp ?? internalRef
  const containerRef = useRef<HTMLDivElement>(null)
  const [sel, setSel] = useState(0)
  const [dismissed, setDismissed] = useState(false)
  const [anchor, setAnchor] = useState<{ left: number; bottom: number } | null>(null)
  // Collapse a long pill list to a few + "+N more" so they don't flood the
  // field (e.g. a broad namespace scope seeded as ns: pills). Expanding wraps
  // them so every pill stays removable.
  const [pillsExpanded, setPillsExpanded] = useState(false)
  const MAX_VISIBLE_PILLS = 3
  // Reset the expand toggle once there's nothing to collapse (pills cleared or
  // trimmed) — otherwise a later seed of many pills would render expanded,
  // re-flooding the field instead of collapsing.
  useEffect(() => {
    if (pills.length <= MAX_VISIBLE_PILLS + 1) setPillsExpanded(false)
  }, [pills.length])
  const collapsePills = !pillsExpanded && pills.length > MAX_VISIBLE_PILLS + 1
  const shownPills = collapsePills ? pills.slice(0, MAX_VISIBLE_PILLS) : pills
  const hiddenPillCount = pills.length - shownPills.length

  const mod = useMemo(() => activeModifier(text, aliases), [text, aliases])

  const filtered = useMemo(() => {
    if (!mod) return []
    const opts = modifierOptions?.[mod.canon]
    if (!opts) return []
    const p = mod.partial.toLowerCase()
    if (!p) return opts.slice(0, MAX_SUGGESTIONS)
    const pref: string[] = []
    const sub: string[] = []
    for (const o of opts) {
      const lo = o.toLowerCase()
      if (lo.startsWith(p)) pref.push(o)
      else if (lo.includes(p)) sub.push(o)
    }
    return [...pref, ...sub].slice(0, MAX_SUGGESTIONS)
  }, [mod, modifierOptions])

  const suggesting = !dismissed && filtered.length > 0

  useEffect(() => { setSel(0) }, [filtered.length, mod?.canon, mod?.partial])
  useEffect(() => { onSuggestingChange?.(suggesting) }, [suggesting, onSuggestingChange])

  // Anchor the portaled suggestion dropdown under the input box.
  useEffect(() => {
    if (!suggesting) { setAnchor(null); return }
    const update = () => {
      const el = containerRef.current
      if (el) { const r = el.getBoundingClientRect(); setAnchor({ left: r.left, bottom: r.bottom }) }
    }
    update()
    window.addEventListener('resize', update)
    window.addEventListener('scroll', update, true)
    return () => { window.removeEventListener('resize', update); window.removeEventListener('scroll', update, true) }
  }, [suggesting])

  const commitPill = useCallback((key: string, value: string, before: string) => {
    onChange({ pills: [...pills, { key, value }], text: before })
    setDismissed(false)
  }, [onChange, pills])

  const removePill = useCallback((idx: number) => {
    onChange({ pills: pills.filter((_, i) => i !== idx), text })
    inputRef.current?.focus()
  }, [onChange, pills, text, inputRef])

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (suggesting) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setSel((s) => Math.min(s + 1, filtered.length - 1)); return }
      if (e.key === 'ArrowUp') { e.preventDefault(); setSel((s) => Math.max(s - 1, 0)); return }
      if (e.key === 'Enter' || e.key === 'Tab') {
        if (mod && filtered[sel]) { e.preventDefault(); commitPill(mod.canon, filtered[sel], mod.before); return }
      }
      if (e.key === 'Escape') { e.preventDefault(); setDismissed(true); return }
    }
    if (e.key === ' ' && mod && mod.partial.length > 0) {
      e.preventDefault()
      commitPill(mod.canon, mod.partial, mod.before)
      return
    }
    if (e.key === 'Backspace' && text === '' && pills.length > 0) {
      e.preventDefault()
      const last = pills[pills.length - 1]
      onChange({ pills: pills.slice(0, -1), text: `${last.key}:${last.value}` })
      return
    }
    onKeyDown?.(e)
  }, [suggesting, filtered, sel, mod, text, pills, commitPill, onChange, onKeyDown])

  return (
    <div ref={containerRef} className={clsx('flex items-center gap-1.5', pillsExpanded && 'flex-wrap', className)} onClick={() => inputRef.current?.focus()}>
      {leftSlot}
      {shownPills.map((p, i) => (
        <span key={`${p.key}:${p.value}:${i}`} className="inline-flex items-center gap-1 shrink-0 rounded-md bg-theme-elevated border border-theme-border-light pl-1.5 pr-1 py-0.5 text-xs whitespace-nowrap">
          <span className="text-theme-text-tertiary">{p.key}:</span>
          <span className="max-w-[16ch] truncate font-medium text-theme-text-primary" title={p.value}>{p.value}</span>
          <button
            type="button"
            tabIndex={-1}
            aria-label={`Remove ${p.key}:${p.value} filter`}
            onMouseDown={(e) => { e.preventDefault(); removePill(i) }}
            className="text-theme-text-tertiary hover:text-theme-text-primary rounded"
          >
            <X className="w-3 h-3" />
          </button>
        </span>
      ))}
      {collapsePills && hiddenPillCount > 0 && (
        <button
          type="button"
          tabIndex={-1}
          onMouseDown={(e) => { e.preventDefault(); setPillsExpanded(true) }}
          className="shrink-0 rounded-md bg-theme-elevated border border-theme-border-light px-1.5 py-0.5 text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary whitespace-nowrap"
          title="Show all filters"
        >
          +{hiddenPillCount} more
        </button>
      )}
      {pillsExpanded && pills.length > MAX_VISIBLE_PILLS + 1 && (
        <button
          type="button"
          tabIndex={-1}
          onMouseDown={(e) => { e.preventDefault(); setPillsExpanded(false) }}
          className="shrink-0 rounded-md px-1.5 py-0.5 text-xs text-theme-text-tertiary hover:text-theme-text-primary whitespace-nowrap"
        >
          less
        </button>
      )}
      <Input
        ref={inputRef}
        value={text}
        onChange={(e) => { setDismissed(false); onChange({ text: e.target.value, pills }) }}
        onKeyDown={handleKeyDown}
        onFocus={onFocus}
        placeholder={pills.length ? '' : placeholder}
        aria-label={rest['aria-label']}
        className={clsx(
          'flex-1 min-w-[80px] bg-transparent text-theme-text-primary placeholder-theme-text-tertiary outline-none',
          inputClassName ?? 'text-sm',
        )}
      />
      {rightSlot}
      {suggesting && anchor && mod && createPortal(
        <div
          style={{ position: 'fixed', top: anchor.bottom + 4, left: anchor.left, width: 280, maxWidth: 'calc(100vw - 2rem)' }}
          className="z-[130] dialog shadow-theme-lg overflow-hidden"
        >
          <div className="px-2.5 py-1 text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary">{mod.canon}</div>
          <div className="max-h-56 overflow-y-auto pb-1">
            {filtered.map((opt, i) => (
              <button
                key={opt}
                type="button"
                onMouseDown={(e) => { e.preventDefault(); e.stopPropagation(); commitPill(mod.canon, opt, mod.before) }}
                onMouseMove={() => setSel(i)}
                className={clsx('w-full text-left px-2.5 py-1 text-sm truncate', i === sel ? 'selection' : 'hover:bg-theme-elevated/40 text-theme-text-primary')}
              >
                {highlightPartial(opt, mod.partial)}
              </button>
            ))}
          </div>
        </div>,
        document.body,
      )}
    </div>
  )
}
