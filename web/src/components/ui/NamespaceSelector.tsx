import { useState, useRef, useEffect, useCallback, useMemo } from 'react'
import { createPortal } from 'react-dom'
import { clsx } from 'clsx'
import { ChevronDown, Search, X, Check, Shield } from 'lucide-react'
import { Tooltip } from './Tooltip'
import { isForbiddenError } from '../../api/client'

interface Namespace {
  name: string
}

interface NamespaceSelectorProps {
  value: string[]
  onChange: (value: string[]) => void
  namespaces: Namespace[] | undefined
  namespacesError?: Error | null
  className?: string
  disabled?: boolean
  disabledTooltip?: string
}

export function NamespaceSelector({
  value,
  onChange,
  namespaces,
  namespacesError,
  className,
  disabled,
  disabledTooltip,
}: NamespaceSelectorProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [manualInput, setManualInput] = useState('')
  const [highlightedIndex, setHighlightedIndex] = useState(0)
  const [dropdownPosition, setDropdownPosition] = useState({ top: 0, left: 0, width: 0 })

  const triggerRef = useRef<HTMLButtonElement>(null)
  const dropdownRef = useRef<HTMLDivElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)

  const isForbidden = isForbiddenError(namespacesError)

  // Convert value to Set for efficient lookups
  const selectedSet = useMemo(() => new Set(value), [value])

  // Sort and filter namespaces
  const sortedNamespaces = useMemo(() => {
    if (!namespaces) return []
    return [...namespaces].sort((a, b) => a.name.localeCompare(b.name))
  }, [namespaces])

  const filteredNamespaces = useMemo(() => {
    if (!search.trim()) return sortedNamespaces
    const searchLower = search.toLowerCase()
    return sortedNamespaces.filter((ns) =>
      ns.name.toLowerCase().includes(searchLower)
    )
  }, [sortedNamespaces, search])

  // Update position when dropdown opens
  const updatePosition = useCallback(() => {
    if (!triggerRef.current) return
    const rect = triggerRef.current.getBoundingClientRect()
    const dropdownWidth = Math.max(rect.width, 220) // Minimum width for checkboxes
    // Align dropdown to the right edge of the button
    const left = rect.right - dropdownWidth
    setDropdownPosition({
      top: rect.bottom + 4,
      left: Math.max(8, left), // Ensure at least 8px from screen edge
      width: dropdownWidth,
    })
  }, [])

  // Open dropdown
  const openDropdown = useCallback(() => {
    setIsOpen(true)
    setSearch('')
    setHighlightedIndex(0)
    updatePosition()
  }, [updatePosition])

  // Close dropdown
  const closeDropdown = useCallback(() => {
    setIsOpen(false)
    setSearch('')
  }, [])

  // Toggle a namespace selection
  const toggleNamespace = useCallback((ns: string) => {
    if (selectedSet.has(ns)) {
      onChange(value.filter((v) => v !== ns))
    } else {
      onChange([...value, ns])
    }
  }, [selectedSet, value, onChange])

  // Select all visible namespaces
  const selectAll = useCallback(() => {
    const allNames = sortedNamespaces.map((ns) => ns.name)
    onChange(allNames)
  }, [sortedNamespaces, onChange])

  // Clear all selections (shows all namespaces)
  const clearAll = useCallback(() => {
    onChange([])
  }, [onChange])

  // Add a manually typed namespace
  const addManualNamespace = useCallback(() => {
    const ns = manualInput.trim()
    if (ns && !selectedSet.has(ns)) {
      onChange([...value, ns])
    }
    setManualInput('')
  }, [manualInput, selectedSet, value, onChange])

  // Focus search input when dropdown opens
  useEffect(() => {
    if (isOpen) {
      // Small delay to ensure the dropdown is rendered
      requestAnimationFrame(() => {
        searchInputRef.current?.focus()
      })
    }
  }, [isOpen])

  // Reset highlighted index when filtered options change
  useEffect(() => {
    setHighlightedIndex(0)
  }, [filteredNamespaces])

  // Handle click outside
  useEffect(() => {
    if (!isOpen) return

    const handleClickOutside = (e: MouseEvent) => {
      const target = e.target as Node
      if (
        triggerRef.current?.contains(target) ||
        dropdownRef.current?.contains(target)
      ) {
        return
      }
      closeDropdown()
    }

    // Small delay to prevent immediate close on open click
    const timeoutId = setTimeout(() => {
      document.addEventListener('mousedown', handleClickOutside)
    }, 0)

    return () => {
      clearTimeout(timeoutId)
      document.removeEventListener('mousedown', handleClickOutside)
    }
  }, [isOpen, closeDropdown])

  // Handle keyboard navigation
  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setHighlightedIndex((prev) =>
          prev < filteredNamespaces.length - 1 ? prev + 1 : prev
        )
        break
      case 'ArrowUp':
        e.preventDefault()
        setHighlightedIndex((prev) => (prev > 0 ? prev - 1 : 0))
        break
      case 'Enter':
      case ' ':
        e.preventDefault()
        if (filteredNamespaces[highlightedIndex]) {
          toggleNamespace(filteredNamespaces[highlightedIndex].name)
        }
        break
      case 'Escape':
        e.preventDefault()
        closeDropdown()
        break
      case 'Tab':
        closeDropdown()
        break
    }
  }, [filteredNamespaces, highlightedIndex, toggleNamespace, closeDropdown])

  // Scroll highlighted item into view
  useEffect(() => {
    if (!isOpen || !dropdownRef.current) return
    const highlighted = dropdownRef.current.querySelector('[data-highlighted="true"]')
    if (highlighted) {
      highlighted.scrollIntoView({ block: 'nearest' })
    }
  }, [highlightedIndex, isOpen])

  // Get display value
  const displayValue = useMemo(() => {
    if (value.length === 0) return 'All Namespaces'
    if (value.length === 1) return value[0]
    return `${value.length} namespaces`
  }, [value])

  const allSelected = sortedNamespaces.length > 0 && value.length === sortedNamespaces.length

  return (
    <>
      <Tooltip content={disabledTooltip} disabled={!disabled} position="bottom">
        <button
          ref={triggerRef}
          type="button"
          disabled={disabled}
          onClick={() => (isOpen ? closeDropdown() : openDropdown())}
          className={clsx(
            'appearance-none bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1 pr-6 border border-theme-border-light',
            'focus:outline-none focus:ring-1 focus:ring-blue-500 min-w-[100px] text-left relative',
            'transition-colors',
            disabled ? 'opacity-50 cursor-not-allowed' : 'hover:bg-theme-hover',
            className
          )}
        >
          <span className="block truncate">{displayValue}</span>
          <ChevronDown
            className={clsx(
              'absolute right-1.5 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-secondary transition-transform',
              isOpen && 'rotate-180'
            )}
          />
        </button>
      </Tooltip>

      {isOpen &&
        createPortal(
          <>
          {/* Backdrop to capture clicks outside the dropdown */}
          <div
            className="fixed inset-0 z-[9998]"
            onClick={closeDropdown}
          />
          <div
            ref={dropdownRef}
            className="fixed z-[9999] bg-theme-elevated border border-theme-border rounded-md shadow-lg overflow-hidden"
            style={{
              top: dropdownPosition.top,
              left: dropdownPosition.left,
              width: dropdownPosition.width,
            }}
            onKeyDown={handleKeyDown}
          >
            {isForbidden ? (
              <>
                {/* Manual namespace input when listing is forbidden */}
                <div className="p-2 border-b border-theme-border">
                  <div className="flex items-center gap-1.5 text-[10px] text-amber-400 mb-2">
                    <Shield className="w-3 h-3" />
                    <span>Cannot list namespaces — type a name</span>
                  </div>
                  <div className="flex gap-1">
                    <input
                      ref={searchInputRef}
                      type="text"
                      value={manualInput}
                      onChange={(e) => setManualInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.preventDefault()
                          addManualNamespace()
                        } else if (e.key === 'Escape') {
                          closeDropdown()
                        }
                      }}
                      placeholder="Namespace name..."
                      className="flex-1 bg-theme-base text-theme-text-primary text-xs rounded px-2 py-1.5 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500 placeholder:text-theme-text-tertiary"
                    />
                    <button
                      type="button"
                      onClick={addManualNamespace}
                      disabled={!manualInput.trim()}
                      className="px-2 py-1 text-xs btn-brand rounded"
                    >
                      Add
                    </button>
                  </div>
                </div>

                {/* Show currently selected namespaces with remove button */}
                <div className="max-h-[200px] overflow-y-auto">
                  {value.length === 0 ? (
                    <div className="px-3 py-4 text-center text-xs text-theme-text-tertiary">
                      All namespaces (type a name to filter)
                    </div>
                  ) : (
                    value.map((ns) => (
                      <div
                        key={ns}
                        className="w-full text-left px-3 py-1.5 text-xs flex items-center justify-between gap-2 text-theme-text-primary hover:bg-theme-hover"
                      >
                        <span className="truncate">{ns}</span>
                        <button
                          type="button"
                          onClick={() => onChange(value.filter(v => v !== ns))}
                          className="text-theme-text-tertiary hover:text-red-400 flex-shrink-0"
                        >
                          <X className="w-3 h-3" />
                        </button>
                      </div>
                    ))
                  )}
                </div>

                {value.length > 0 && (
                  <div className="px-3 py-1.5 text-[10px] text-theme-text-tertiary border-t border-theme-border bg-theme-base flex justify-between">
                    <span>{value.length} selected</span>
                    <button type="button" onClick={clearAll} className="text-blue-400 hover:text-blue-300">
                      Clear all
                    </button>
                  </div>
                )}
              </>
            ) : (
              <>
                {/* Search input */}
                <div className="p-2 border-b border-theme-border">
                  <div className="relative">
                    <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-theme-text-tertiary" />
                    <input
                      ref={searchInputRef}
                      type="text"
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                      placeholder="Search namespaces..."
                      className="w-full bg-theme-base text-theme-text-primary text-xs rounded px-2 py-1.5 pl-7 pr-7 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500 placeholder:text-theme-text-tertiary"
                    />
                    {search && (
                      <button
                        type="button"
                        onClick={() => setSearch('')}
                        className="absolute right-2 top-1/2 -translate-y-1/2 text-theme-text-tertiary hover:text-theme-text-secondary"
                      >
                        <X className="w-3.5 h-3.5" />
                      </button>
                    )}
                  </div>
                </div>

                {/* Select All / Clear All buttons */}
                <div className="flex gap-1 px-2 py-1.5 border-b border-theme-border bg-theme-base">
                  <button
                    type="button"
                    onClick={selectAll}
                    disabled={allSelected}
                    className={clsx(
                      'flex-1 text-[10px] px-2 py-1 rounded transition-colors',
                      allSelected
                        ? 'text-theme-text-tertiary cursor-not-allowed'
                        : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
                    )}
                  >
                    Select All
                  </button>
                  <button
                    type="button"
                    onClick={clearAll}
                    disabled={value.length === 0}
                    className={clsx(
                      'flex-1 text-[10px] px-2 py-1 rounded transition-colors',
                      value.length === 0
                        ? 'text-theme-text-tertiary cursor-not-allowed'
                        : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary'
                    )}
                  >
                    Clear All
                  </button>
                </div>

                {/* Options list with checkboxes */}
                <div className="max-h-[240px] overflow-y-auto">
                  {filteredNamespaces.length === 0 ? (
                    <div className="px-3 py-6 text-center text-xs text-theme-text-tertiary">
                      No namespaces match "{search}"
                    </div>
                  ) : (
                    filteredNamespaces.map((ns, index) => {
                      const isSelected = selectedSet.has(ns.name)
                      return (
                        <button
                          key={ns.name}
                          type="button"
                          data-highlighted={index === highlightedIndex}
                          onClick={() => toggleNamespace(ns.name)}
                          onMouseEnter={() => setHighlightedIndex(index)}
                          className={clsx(
                            'w-full text-left px-3 py-1.5 text-xs transition-colors flex items-center gap-2',
                            'text-theme-text-primary',
                            index === highlightedIndex && 'bg-theme-hover'
                          )}
                        >
                          <div
                            className={clsx(
                              'w-3.5 h-3.5 rounded border flex items-center justify-center flex-shrink-0',
                              isSelected
                                ? 'bg-blue-500 border-blue-500'
                                : 'border-theme-border-light bg-theme-base'
                            )}
                          >
                            {isSelected && <Check className="w-2.5 h-2.5 text-white" />}
                          </div>
                          <span className="truncate">{ns.name}</span>
                        </button>
                      )
                    })
                  )}
                </div>

                {/* Namespace count and selection info */}
                {sortedNamespaces.length > 0 && (
                  <div className="px-3 py-1.5 text-[10px] text-theme-text-tertiary border-t border-theme-border bg-theme-base flex justify-between">
                    <span>
                      {filteredNamespaces.length === sortedNamespaces.length
                        ? `${sortedNamespaces.length} namespaces`
                        : `${filteredNamespaces.length} of ${sortedNamespaces.length} namespaces`}
                    </span>
                    {value.length > 0 && (
                      <span className="text-blue-400">{value.length} selected</span>
                    )}
                  </div>
                )}
              </>
            )}
          </div>
          </>,
          document.body
        )}
    </>
  )
}
