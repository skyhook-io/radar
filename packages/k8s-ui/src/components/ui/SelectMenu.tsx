import { useEffect, useMemo, useRef, useState } from 'react'
import { Check, ChevronDown, Search } from 'lucide-react'
import { clsx } from 'clsx'

export interface SelectMenuOption {
  value: string
  label: string
}

export function SelectMenu({
  value,
  options,
  onChange,
  ariaLabel,
  className,
  searchPlaceholder,
}: {
  value: string
  options: SelectMenuOption[]
  onChange: (value: string) => void
  ariaLabel: string
  className?: string
  searchPlaceholder?: string
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const rootRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const selected = options.find((option) => option.value === value) ?? options[0]
  const filteredOptions = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    if (!normalized) return options
    return options.filter((option) => option.label.toLowerCase().includes(normalized))
  }, [options, query])

  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  useEffect(() => {
    if (!open) setQuery('')
  }, [open])

  return (
    <div ref={rootRef} className={clsx('relative', className)}>
      <button
        type="button"
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        className="flex h-8 w-full items-center justify-between gap-2 rounded-md border border-theme-border bg-theme-elevated px-2.5 text-xs text-theme-text-primary transition-colors hover:bg-theme-hover"
      >
        <span className="truncate">{selected?.label}</span>
        <ChevronDown className={clsx('h-3.5 w-3.5 shrink-0 text-theme-text-tertiary transition-transform', open && 'rotate-180')} />
      </button>
      {open && (
        <div
          className={clsx(
            'absolute top-full z-50 mt-1 min-w-full overflow-hidden rounded-md border border-theme-border bg-theme-surface shadow-theme-lg',
            searchPlaceholder ? 'left-0 right-0' : 'right-0'
          )}
        >
          {searchPlaceholder && (
            <div className="flex items-center gap-2 border-b border-theme-border px-2.5 py-2">
              <Search className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
              <input
                autoFocus
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' && filteredOptions.length === 1) {
                    event.preventDefault()
                    onChange(filteredOptions[0].value)
                    setOpen(false)
                  } else if (event.key === 'ArrowDown') {
                    event.preventDefault()
                    listRef.current?.querySelector<HTMLButtonElement>('[role="option"]')?.focus()
                  }
                }}
                aria-label={searchPlaceholder}
                placeholder={searchPlaceholder}
                className="min-w-0 flex-1 bg-transparent text-xs text-theme-text-primary outline-none placeholder:text-theme-text-tertiary"
              />
            </div>
          )}
          <div ref={listRef} role="listbox" className="max-h-64 overflow-y-auto py-1">
            {filteredOptions.length === 0 && (
              <p className="px-3 py-2 text-xs text-theme-text-tertiary">No matches.</p>
            )}
            {filteredOptions.map((option) => {
              const active = option.value === value
              return (
                <button
                  key={option.value}
                  type="button"
                  role="option"
                  aria-selected={active}
                  onClick={() => {
                    onChange(option.value)
                    setOpen(false)
                  }}
                  className={clsx(
                    'flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-xs text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary',
                    !searchPlaceholder && 'whitespace-nowrap'
                  )}
                >
                  <Check className={clsx('h-3.5 w-3.5 shrink-0 text-accent', !active && 'opacity-0')} />
                  <span className={clsx(searchPlaceholder && 'truncate')}>{option.label}</span>
                </button>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}
