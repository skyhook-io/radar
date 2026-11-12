import type { GitOpsOutsideChange } from '../../types/gitops-outside'
import { formatRelativeAgeTime } from '../../utils/format'
import { Badge } from '../ui/Badge'
import { Tooltip } from '../ui/Tooltip'

// OutsideEditChip marks a Flux-managed resource whose fields a person's tool
// wrote. It names the newest edit; the tooltip lists every edited field.
export function OutsideEditChip({ changes }: { changes: GitOpsOutsideChange[] }) {
  const edits = changes.filter((c) => !c.harmless && c.paths.length > 0)
  if (edits.length === 0) return null
  // An entry without a time sorts last.
  const at = (c: GitOpsOutsideChange) => Date.parse(c.time ?? '') || -Infinity
  const newest = edits.reduce((a, b) => (at(b) > at(a) ? b : a))
  const age = newest.time ? `, ${formatRelativeAgeTime(newest.time)}` : ''
  return (
    <Tooltip
      delay={200}
      content={
        <div className="space-y-0.5 text-xs">
          {edits.map((c, i) => (
            <div key={i}>
              <span className="font-medium">{c.tool}</span>: {c.paths.join(', ')}
              {c.morePaths > 0 && ` +${c.morePaths} more`}
            </div>
          ))}
        </div>
      }
    >
      {/* Only the path shortens: the tool and the age are what the chip is for. */}
      <Badge severity="warning" size="sm" className="inline-flex max-w-full items-baseline whitespace-nowrap">
        Edited outside Flux ·&nbsp;<span className="min-w-0 max-w-[18rem] truncate">{newest.paths[0]}</span>&nbsp;· {newest.tool}{age}
      </Badge>
    </Tooltip>
  )
}
