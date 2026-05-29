import { useEffect, useState } from 'react'
import { clsx } from 'clsx'
import { formatUpdatedAgo, msToNextBucket } from '../../utils/format'

interface UpdatedAtLabelProps {
  // Epoch ms of the last successful load — e.g. React Query's dataUpdatedAt.
  dataUpdatedAt: number
  className?: string
}

// Standalone "Updated N ago" freshness label. Re-renders exactly when the
// displayed bucket would change (not every second) via msToNextBucket. Pair it
// with a host-owned refresh button (the GitOps table and Audit header both do).
export function UpdatedAtLabel({ dataUpdatedAt, className }: UpdatedAtLabelProps) {
  const [, force] = useState(0)

  useEffect(() => {
    if (dataUpdatedAt <= 0) return
    let id: ReturnType<typeof setTimeout>
    function schedule() {
      const delay = Math.max(1000, msToNextBucket(Date.now() - dataUpdatedAt))
      id = setTimeout(() => {
        force((t) => t + 1)
        schedule()
      }, delay)
    }
    schedule()
    return () => clearTimeout(id)
  }, [dataUpdatedAt])

  const label = dataUpdatedAt > 0 ? `Updated ${formatUpdatedAgo(Date.now() - dataUpdatedAt)}` : 'Not yet loaded'

  return (
    <span className={clsx('whitespace-nowrap tabular-nums text-xs text-theme-text-tertiary', className)}>
      {label}
    </span>
  )
}
