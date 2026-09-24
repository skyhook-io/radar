import { Bell, Users, X } from 'lucide-react'
import { dismissCloudHint, openCloudFunnel, type CloudAlertSubject } from './cloudHints'
import { Tooltip } from '../ui/Tooltip'

// A small labeled group on a problem assessment: what Radar Cloud adds once an
// investigation has found something (an alert when it recurs, the team on the
// same resource). One X hides it on every investigation for good, since the
// card repeats on every run and a per-card hide would just come back. Callers
// gate on useCloudHintsEnabled and on the assessment being a problem (a
// healthy result has nothing to alert on), and own the dismissed state so the
// slot around it can disappear too. "Alert me" needs the issue the run started
// from: a run on a bare resource has no issue that could happen again.
export function FindingsCloudActions({ subject, onDismiss }: { subject: CloudAlertSubject; onDismiss: () => void }) {
  return (
    <div className="flex items-center gap-0.5 whitespace-nowrap border-l border-theme-border pl-2.5" data-findings-cloud-actions>
      <span className="mr-1 text-[11px] text-theme-text-tertiary">Radar Cloud</span>
      {subject.issueId && (
        <button
          type="button"
          onClick={() => openCloudFunnel('alert-findings', { ...subject, intent: 'alert' })}
          className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
        >
          <Bell className="h-3.5 w-3.5" aria-hidden />
          Alert me
        </button>
      )}
      <button
        type="button"
        onClick={() => openCloudFunnel('team-findings', { ...subject, intent: 'team' })}
        className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
      >
        <Users className="h-3.5 w-3.5" aria-hidden />
        Investigate with team
      </button>
      <Tooltip content="Hide Radar Cloud actions" delay={100} wrapperClassName="shrink-0">
        <button
          type="button"
          aria-label="Hide Radar Cloud actions"
          onClick={() => {
            dismissCloudHint('alert-findings')
            onDismiss()
          }}
          className="rounded-md p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </Tooltip>
    </div>
  )
}
