// Shape-stable loading stand-ins for the list-board chassis (GitOps,
// Applications). Both mirror the loaded anatomy so data resolves in place:
// rows appear inside the table frame, facets inside the rail — no half-height
// rail dangling next to a centered spinner, no layout jump when the response
// lands.

export function BoardTableSkeleton({ rows = 10 }: { rows?: number }) {
  return (
    <div aria-live="polite" aria-label="Loading…" className="divide-y divide-theme-border-light">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex items-center gap-6 px-4 py-3" style={{ opacity: 1 - i * 0.09 }}>
          <div className="min-w-0 flex-[2] space-y-2">
            <div className="h-4 w-2/3 animate-pulse rounded bg-theme-hover" />
            <div className="h-3 w-1/2 animate-pulse rounded bg-theme-hover" />
          </div>
          <div className="hidden h-4 flex-1 animate-pulse rounded bg-theme-hover md:block" />
          <div className="h-5 w-20 animate-pulse rounded-full bg-theme-hover" />
          <div className="h-5 w-20 animate-pulse rounded-full bg-theme-hover" />
          <div className="hidden h-4 flex-[1.5] animate-pulse rounded bg-theme-hover lg:block" />
        </div>
      ))}
    </div>
  )
}

// Section stubs sized like the loaded rail so it keeps its height while
// counts are unknown — real facet buttons with "0" counts would be false
// zeros. `sections` = [title, rowCount] pairs approximating the loaded rail.
export function BoardRailSkeleton({ sections }: { sections: Array<[string, number]> }) {
  return (
    <div aria-hidden>
      {sections.map(([title, rows]) => (
        <div key={title} className="border-b border-theme-border-light px-3 py-3">
          <div className="mb-2 h-3 w-24 animate-pulse rounded bg-theme-hover" />
          <div className="space-y-1.5">
            {Array.from({ length: rows }, (_, i) => (
              <div key={i} className="flex items-center justify-between px-1.5 py-1">
                <div className="h-3.5 animate-pulse rounded bg-theme-hover" style={{ width: `${52 - i * 6}%` }} />
                <div className="h-3.5 w-6 animate-pulse rounded bg-theme-hover" />
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}
