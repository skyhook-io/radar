import type { GitOpsOutsideChange } from '../../types/gitops-outside'
import { formatRelativeAgeTime } from '../../utils/format'
import { Badge } from '../ui/Badge'
import { Tooltip } from '../ui/Tooltip'

// OutsideEditChip marks a Flux-managed resource whose fields a person's tool
// wrote. It names the newest edit; the tooltip lists every edited field.
export function OutsideEditChip({ changes }: { changes: GitOpsOutsideChange[] }) {
  const edits = changes.filter((c) => !c.harmless && c.paths.length > 0)
  if (edits.length === 0) return null
  const newest = edits.reduce((a, b) => (Date.parse(b.time ?? '') > Date.parse(a.time ?? '') ? b : a))
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
      <Badge severity="warning" size="sm">
        Edited outside Flux · {newest.paths[0]} · {newest.tool}{age}
      </Badge>
    </Tooltip>
  )
}
