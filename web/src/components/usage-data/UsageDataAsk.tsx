import { useEffect, useRef, useState } from 'react'
import { BarChart3, Check } from 'lucide-react'
import { markUsagePromptShown, useSetUsageData, type UsageDataStatus } from '../../api/telemetry'

// One wording for every place Radar asks, so the question can't drift.
export function UsageDataBlurb({ onReadMore, shared = false }: { onReadMore: () => void; shared?: boolean }) {
  return (
    <>
      <p className="text-sm font-medium text-theme-text-primary">Help improve Radar</p>
      <p className="mt-0.5 text-xs text-theme-text-secondary leading-relaxed">
        {shared ? 'Send anonymous usage stats from this shared Radar.' : 'Send anonymous usage stats.'}{' '}
        <button
          type="button"
          onClick={onReadMore}
          className="text-theme-text-primary underline underline-offset-2 hover:text-accent-text"
        >
          Read more
        </button>
      </p>
    </>
  )
}

// The usage-data question, asked inside What's New when the server says to:
// someone who closed it without answering is asked again only months later,
// and a "no" is never asked again. Anyone who can't decide (a shared Radar's
// non-owners, env-managed, DO_NOT_TRACK) never sees it.
export function UsageDataAsk({ usageData, onReadMore }: {
  usageData: UsageDataStatus | undefined
  onReadMore: () => void
}) {
  const setUsageData = useSetUsageData()
  const [answer, setAnswer] = useState<boolean | null>(null)
  // Latched: recording the showing stops the server offering the question,
  // and the block must not vanish while the dialog is open.
  const [offered, setOffered] = useState(!!usageData?.ask)
  const recorded = useRef(false)

  useEffect(() => {
    if (usageData?.ask) setOffered(true)
  }, [usageData?.ask])
  useEffect(() => {
    if (offered && !recorded.current) {
      recorded.current = true
      markUsagePromptShown()
    }
  }, [offered])

  const undecided = offered && usageData?.state === 'undecided' && usageData.canChange
  if (!undecided && answer === null) return null

  // The answer stays on screen so the block doesn't vanish under the cursor.
  if (answer !== null) {
    return (
      <div className="flex items-center gap-2 px-6 py-3 border-t border-theme-border text-xs text-theme-text-secondary">
        <Check className="w-3.5 h-3.5 shrink-0 text-accent" aria-hidden />
        {answer
          ? 'Thanks. Change this any time in Settings > Privacy.'
          : 'Nothing will be sent. Change this any time in Settings > Privacy.'}
      </div>
    )
  }

  const choose = (enabled: boolean) => setUsageData.mutate(enabled, { onSuccess: () => setAnswer(enabled) })

  return (
    <div className="flex flex-col sm:flex-row sm:items-center gap-3 px-6 py-3.5 border-t border-theme-border">
      <div className="flex gap-3 min-w-0 flex-1">
        <span className="flex items-center justify-center w-8 h-8 shrink-0 rounded-lg bg-accent-muted text-accent">
          <BarChart3 className="w-4 h-4" aria-hidden />
        </span>
        <div className="min-w-0">
          <UsageDataBlurb onReadMore={onReadMore} shared={usageData?.shared} />
        </div>
      </div>
      <div className="flex items-center gap-2 shrink-0 pl-11 sm:pl-0">
        <button
          type="button"
          disabled={setUsageData.isPending}
          onClick={() => choose(true)}
          className="px-3 py-1.5 text-xs font-medium rounded-lg border border-theme-border bg-theme-surface text-theme-text-primary hover:bg-theme-hover disabled:opacity-50"
        >
          Send usage stats
        </button>
        <button
          type="button"
          disabled={setUsageData.isPending}
          onClick={() => choose(false)}
          className="px-3 py-1.5 text-xs font-medium rounded-lg text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated disabled:opacity-50"
        >
          No thanks
        </button>
      </div>
    </div>
  )
}
