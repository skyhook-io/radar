import { useMemo } from 'react'
import { CertaintyGlyph, ProblemList, StatusDot, type LonghornVolumeHealth, type ResourceRef } from '@skyhook-io/k8s-ui'
import { refToSelectedResource } from '../../utils/navigation'
import { ScreenBody } from '../workspace/layout'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { longhornAttention, longhornVolumesPartial } from './attention'
import { LONGHORN_VOLUMES } from './routes'
import { LONGHORN_HEALTH, LonghornCoverageNotice, LonghornHeader, type LonghornScreenProps } from './shared'

export const LONGHORN_NOTHING_NEEDS_ATTENTION = 'No volume is faulted or degraded.'
export const LONGHORN_NOTHING_IN_WHAT_WAS_READ = 'No volume Radar can read is faulted or degraded.'

const HEALTH_ORDER: LonghornVolumeHealth[] = ['faulted', 'degraded', 'healthy', 'detached', 'unknown']

export function LonghornOverview({ data, onInspect }: LonghornScreenProps) {
  const navigateTo = useWorkspaceNavigate()
  const attention = useMemo(() => longhornAttention(data), [data])
  const partial = longhornVolumesPartial(data)
  const navigate = (ref: ResourceRef) => onInspect(refToSelectedResource(ref))

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <LonghornHeader title="Overview" data={data} />
      <ScreenBody>
        <LonghornCoverageNotice data={data} />
        <dl className="flex flex-wrap gap-x-7 gap-y-2 text-sm" aria-label="Longhorn volume health">
          {HEALTH_ORDER.map((h) => (
            <div key={h} className="flex items-center gap-1.5">
              <StatusDot tone={data.counts ? LONGHORN_HEALTH[h].tone : 'unknown'} />
              <dt className="text-theme-text-tertiary">{LONGHORN_HEALTH[h].label}:</dt>
              <dd className="flex items-center gap-1 font-medium text-theme-text-primary">
                {data.counts ? (
                  <>
                    {!data.counts.complete && <CertaintyGlyph certainty="lower_bound" title="At least this many: Radar couldn’t read every Longhorn volume" />}
                    <button type="button" onClick={() => navigateTo(`${LONGHORN_VOLUMES.path}?health=${h}`)} className="hover:underline">
                      {data.counts[h]}
                    </button>
                  </>
                ) : (
                  <span className="text-theme-text-tertiary">unknown — no volume read</span>
                )}
              </dd>
            </div>
          ))}
        </dl>
        <section aria-labelledby="longhorn-attention">
          <h2 id="longhorn-attention" className="mb-2 text-sm font-semibold text-theme-text-primary">Needs attention</h2>
          <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
            {attention.length === 0 ? (
              <p className="text-sm text-theme-text-tertiary">{partial ? LONGHORN_NOTHING_IN_WHAT_WAS_READ : LONGHORN_NOTHING_NEEDS_ATTENTION}</p>
            ) : (
              <ProblemList problems={attention} rootKind="" onNavigate={navigate} hideOrigin subjectFirst />
            )}
          </div>
        </section>
      </ScreenBody>
    </div>
  )
}
