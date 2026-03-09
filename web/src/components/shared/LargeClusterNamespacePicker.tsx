import { useState, useRef, useEffect, useMemo } from 'react'
import { Search } from 'lucide-react'

export function LargeClusterNamespacePicker({ namespaces, onSelect }: {
  namespaces: { name: string }[] | undefined
  onSelect: (ns: string) => void
}) {
  const [search, setSearch] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  const sorted = useMemo(() => {
    if (!namespaces) return []
    return [...namespaces].sort((a, b) => a.name.localeCompare(b.name))
  }, [namespaces])

  const filtered = useMemo(() => {
    if (!search.trim()) return sorted
    const q = search.toLowerCase()
    return sorted.filter(ns => ns.name.toLowerCase().includes(q))
  }, [sorted, search])

  return (
    <div className="text-left">
      <div className="relative mb-2">
        <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
        <input
          ref={inputRef}
          type="text"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search namespaces..."
          className="w-full bg-theme-base text-theme-text-primary text-sm rounded-lg px-3 py-2 pl-9 border border-theme-border-light focus:outline-none focus:ring-2 focus:ring-blue-500/50 focus:border-blue-500/50 placeholder:text-theme-text-tertiary"
        />
      </div>
      <div className="max-h-[240px] overflow-y-auto rounded-lg border border-theme-border bg-theme-base">
        {!namespaces ? (
          <div className="px-3 py-6 text-center text-sm text-theme-text-tertiary">
            Loading namespaces...
          </div>
        ) : filtered.length === 0 ? (
          <div className="px-3 py-6 text-center text-sm text-theme-text-tertiary">
            No namespaces match &ldquo;{search}&rdquo;
          </div>
        ) : (
          filtered.map((ns) => (
            <button
              key={ns.name}
              type="button"
              onClick={() => onSelect(ns.name)}
              className="w-full text-left px-3 py-2 text-sm text-theme-text-primary hover:bg-theme-hover transition-colors border-b border-theme-border last:border-b-0"
            >
              {ns.name}
            </button>
          ))
        )}
      </div>
      {sorted.length > 0 && (
        <p className="mt-2 text-xs text-theme-text-tertiary text-center">
          {filtered.length === sorted.length
            ? `${sorted.length} namespaces`
            : `${filtered.length} of ${sorted.length} namespaces`}
        </p>
      )}
    </div>
  )
}
