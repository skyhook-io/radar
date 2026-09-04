import { useState } from 'react'
import { ChevronDown, ChevronRight, Box } from 'lucide-react'
import { clsx } from 'clsx'
import { formatAge } from '../../resource-utils'
import { revisionRoleBadges } from '../../../shared/ResourceActionsBar'
import type { WorkloadRevision, WorkloadPodInfo } from '../../../../types'

interface ReplicaSetProgressionProps {
  revisions: WorkloadRevision[]
  pods?: WorkloadPodInfo[]
  isRollout: true
  namespace: string
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function imageTag(image: string): string {
  const lastColon = image.lastIndexOf(':')
  const lastSlash = image.lastIndexOf('/')
  return lastColon > lastSlash ? image.slice(lastColon + 1) : image
}

function podToneClass(pod: WorkloadPodInfo): string {
  switch (pod.healthLevel) {
    case 'healthy':
      return 'status-healthy'
    case 'degraded':
      return 'status-degraded'
    case 'unhealthy':
      return 'status-unhealthy'
    default:
      return 'status-unknown'
  }
}

// Read-only revision tree: Rollout -> ReplicaSet revision -> pods, mirroring
// `kubectl argo rollouts get rollout`'s tree output. Newest first, matching
// the existing rollback-history dialog's ordering. Rollback stays exclusive
// to that dialog — this is purely informational.
export function ReplicaSetProgression({ revisions, pods, namespace, onNavigate }: ReplicaSetProgressionProps) {
  const [expanded, setExpanded] = useState<Set<number>>(new Set())

  const podsByHash = new Map<string, WorkloadPodInfo[]>()
  for (const pod of pods ?? []) {
    if (!pod.revisionIdentity) continue
    const list = podsByHash.get(pod.revisionIdentity) ?? []
    list.push(pod)
    podsByHash.set(pod.revisionIdentity, list)
  }

  const toggle = (number: number) =>
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(number)) next.delete(number)
      else next.add(number)
      return next
    })

  return (
    <div className="space-y-1">
      {revisions.map((rev) => {
        const revPods = rev.podHash ? podsByHash.get(rev.podHash) ?? [] : []
        const isOpen = expanded.has(rev.number)
        return (
          <div key={rev.number} className="rounded border border-theme-border/50">
            <button
              type="button"
              onClick={() => toggle(rev.number)}
              className="flex w-full items-center gap-2 px-2 py-1.5 text-left hover:bg-theme-hover"
              disabled={revPods.length === 0}
            >
              {revPods.length > 0 ? (
                isOpen ? <ChevronDown className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" />
              ) : (
                <span className="w-3.5 shrink-0" />
              )}
              <span className="font-mono text-xs text-theme-text-primary">#{rev.number}</span>
              <span className="min-w-0 flex-1 truncate font-mono text-xs text-theme-text-secondary">{imageTag(rev.image)}</span>
              <div className="flex shrink-0 items-center gap-1">
                {revisionRoleBadges(rev, true).map(({ label, tone, tip }) => (
                  <span key={label} title={tip} className={clsx('badge', tone)}>
                    {label}
                  </span>
                ))}
              </div>
              <span className="shrink-0 text-xs tabular-nums text-theme-text-tertiary">{rev.replicas ?? 0} replicas</span>
              <span className="shrink-0 text-xs text-theme-text-tertiary">{formatAge(rev.createdAt)}</span>
            </button>
            {isOpen && revPods.length > 0 && (
              <div className="space-y-0.5 border-t border-theme-border/50 px-2 py-1.5 pl-8">
                {revPods.map((pod) => (
                  <div key={pod.name} className="flex items-center gap-2 py-0.5 text-xs">
                    <Box className="h-3 w-3 shrink-0 text-theme-text-tertiary" />
                    {onNavigate ? (
                      <button
                        onClick={() => onNavigate({ kind: 'Pod', namespace, name: pod.name })}
                        className="min-w-0 flex-1 truncate text-left font-mono text-theme-text-secondary hover:text-brand hover:underline"
                      >
                        {pod.name}
                      </button>
                    ) : (
                      <span className="min-w-0 flex-1 truncate font-mono text-theme-text-secondary">{pod.name}</span>
                    )}
                    <span className={clsx('badge-sm', podToneClass(pod))}>{pod.phase || (pod.ready ? 'Ready' : 'Not Ready')}</span>
                    {(pod.restartCount ?? 0) > 0 && (
                      <span className="shrink-0 text-theme-text-tertiary">{pod.restartCount} restarts</span>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
