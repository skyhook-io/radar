import { useEffect, useId, useMemo, useRef, useState } from 'react'
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
  ariaDescribedBy,
  className,
  searchPlaceholder,
  disabled = false,
  id,
}: {
  value: string
  options: SelectMenuOption[]
  onChange: (value: string) => void
  ariaLabel: string
  ariaDescribedBy?: string
  className?: string
  searchPlaceholder?: string
  disabled?: boolean
  id?: string
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [highlightedIndex, setHighlightedIndex] = useState(0)
  const listboxId = useId()
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const pointerDownInsideRef = useRef(false)
  const selected = options.find((option) => option.value === value) ?? options[0]
  const filteredOptions = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    if (!normalized) return options
    return options.filter((option) => option.label.toLowerCase().includes(normalized))
  }, [options, query])
  const selectedIsVisible = filteredOptions.some((option) => option.value === value)

  const selectOption = (nextValue: string) => {
    onChange(nextValue)
    setOpen(false)
    triggerRef.current?.focus()
  }

  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onPointerDown)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
    }
  }, [open])

  useEffect(() => {
    if (!open) setQuery('')
  }, [open])

  useEffect(() => {
    if (!open || !query) return
    if (listRef.current) listRef.current.scrollTop = 0
  }, [open, query])

  useEffect(() => {
    if (!open) return
    if (!searchPlaceholder) {
      triggerRef.current?.focus()
      return
    }
    listRef.current
      ?.querySelector<HTMLElement>('[role="option"][aria-selected="true"]')
      ?.scrollIntoView({ block: 'nearest' })
  }, [open, searchPlaceholder])

  return (
    <div
      ref={rootRef}
      className={clsx('relative', className)}
      onMouseDownCapture={() => {
        pointerDownInsideRef.current = true
        requestAnimationFrame(() => {
          pointerDownInsideRef.current = false
        })
      }}
      onKeyDown={(event) => {
        if (event.key !== 'Escape' || !open) return
        event.preventDefault()
        event.stopPropagation()
        setOpen(false)
        triggerRef.current?.focus()
      }}
      onBlur={(event) => {
        if (!open) return
        if (event.relatedTarget && rootRef.current?.contains(event.relatedTarget)) return
        if (!pointerDownInsideRef.current) {
          setOpen(false)
          return
        }
        requestAnimationFrame(() => {
          if (!document.hasFocus() || !listRef.current) return
          if (searchPlaceholder) searchInputRef.current?.focus()
          else triggerRef.current?.focus()
        })
      }}
    >
      <button
        ref={triggerRef}
        id={id}
        type="button"
        aria-label={ariaLabel}
        aria-describedby={ariaDescribedBy}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listboxId : undefined}
        onClick={() => {
          if (!open) {
            setHighlightedIndex(
              Math.max(
                options.findIndex((option) => option.value === value),
                0,
              ),
            )
          }
          setOpen(!open)
        }}
        disabled={disabled}
        className="flex h-8 w-full items-center justify-between gap-2 rounded-md border border-theme-border bg-theme-elevated px-2.5 text-xs text-theme-text-primary transition-colors hover:bg-theme-hover disabled:cursor-not-allowed disabled:opacity-60 disabled:hover:bg-theme-elevated"
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
            <div
              className="flex items-center gap-2 border-b border-theme-border px-2.5 py-2"
              onMouseDown={(event) => {
                if (event.target !== searchInputRef.current) event.preventDefault()
              }}
            >
              <Search className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
              <input
                ref={searchInputRef}
                autoFocus
                value={query}
                onChange={(event) => {
                  setQuery(event.target.value)
                  setHighlightedIndex(0)
                }}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' && filteredOptions.length > 0) {
                    event.preventDefault()
                    const nextIndex = Math.min(highlightedIndex, filteredOptions.length - 1)
                    selectOption(filteredOptions[nextIndex].value)
                  } else if (event.key === 'ArrowDown') {
                    event.preventDefault()
                    const optionElements = listRef.current?.querySelectorAll<HTMLButtonElement>('[role="option"]')
                    optionElements?.[Math.min(highlightedIndex, optionElements.length - 1)]?.focus()
                  }
                }}
                aria-label={searchPlaceholder}
                role="combobox"
                aria-autocomplete="list"
                aria-controls={listboxId}
                aria-expanded="true"
                aria-activedescendant={
                  filteredOptions.length > 0
                    ? `${listboxId}-option-${Math.min(highlightedIndex, filteredOptions.length - 1)}`
                    : undefined
                }
                placeholder={searchPlaceholder}
                className="min-w-0 flex-1 bg-transparent text-xs text-theme-text-primary outline-none placeholder:text-theme-text-tertiary"
              />
            </div>
          )}
          {searchPlaceholder && (
            <span className="sr-only" aria-live="polite">
              {filteredOptions.length === 0
                ? 'No matches.'
                : `${filteredOptions.length} ${filteredOptions.length === 1 ? 'option' : 'options'} available.`}
            </span>
          )}
          <div
            id={listboxId}
            ref={listRef}
            role="listbox"
            aria-label={ariaLabel}
            onKeyDown={(event) => {
              if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
              const optionElements = Array.from(
                listRef.current?.querySelectorAll<HTMLButtonElement>('[role="option"]') ?? [],
              )
              const currentIndex = optionElements.indexOf(event.target as HTMLButtonElement)
              if (currentIndex < 0) return
              event.preventDefault()
              if (event.key === 'ArrowUp' && currentIndex === 0 && searchPlaceholder) {
                searchInputRef.current?.focus()
                return
              }
              const nextIndex = event.key === 'Home'
                ? 0
                : event.key === 'End'
                  ? optionElements.length - 1
                  : event.key === 'ArrowDown'
                    ? Math.min(currentIndex + 1, optionElements.length - 1)
                    : Math.max(currentIndex - 1, 0)
              optionElements[nextIndex]?.focus()
            }}
            className={clsx(filteredOptions.length > 0 && 'py-1', searchPlaceholder && 'max-h-64 overflow-y-auto')}
          >
            {filteredOptions.map((option, index) => {
              const active = option.value === value
              return (
                <button
                  key={option.value}
                  id={`${listboxId}-option-${index}`}
                  type="button"
                  role="option"
                  aria-selected={active}
                  tabIndex={
                    searchPlaceholder ? (index === highlightedIndex ? 0 : -1) : active || (!selectedIsVisible && index === 0) ? 0 : -1
                  }
                  onClick={() => selectOption(option.value)}
                  onFocus={() => setHighlightedIndex(index)}
                  className={clsx(
                    'flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-xs text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary',
                    searchPlaceholder && index === highlightedIndex && 'bg-theme-hover text-theme-text-primary',
                    !searchPlaceholder && 'whitespace-nowrap'
                  )}
                >
                  <Check className={clsx('h-3.5 w-3.5 shrink-0 text-accent', !active && 'opacity-0')} />
                  <span className={clsx(searchPlaceholder && 'truncate')}>{option.label}</span>
                </button>
              )
            })}
          </div>
          {filteredOptions.length === 0 && (
            <p
              onMouseDown={(event) => event.preventDefault()}
              className="px-3 py-2 text-xs text-theme-text-tertiary"
            >
              No matches.
            </p>
          )}
        </div>
      )}
    </div>
  )
}
