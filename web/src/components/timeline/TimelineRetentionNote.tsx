import { Clock } from 'lucide-react'
import { useTimelineRetentionNote } from '../../context/TimelineCustomization'

// The host's history-limit note as a full-width row above the timeline,
// beside the other history-completeness rows (truncation, stale live
// updates). Informational, not a warning: nothing is broken, the server
// serves this much. Renders nothing when the host has no note.
export function TimelineRetentionBanner() {
  const note = useTimelineRetentionNote()
  if (!note) return null
  return (
    <div
      className="flex flex-wrap items-center gap-x-1.5 gap-y-1 border-b border-theme-border px-4 py-1.5 text-xs text-theme-text-tertiary"
      data-testid="timeline-retention-note"
    >
      <Clock className="h-3.5 w-3.5 shrink-0" />
      {note}
    </div>
  )
}
