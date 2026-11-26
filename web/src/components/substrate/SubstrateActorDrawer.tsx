import { useEffect } from 'react'
import { X } from 'lucide-react'
import { PaneLoader, ResourceDetailDrawer, Tooltip } from '@skyhook-io/k8s-ui'
import { substrateErrorCode, useSubstrateActor, type SubstrateRef } from '../../api/substrateWorkspace'
import { RefreshFailedNotice } from '../workspace/layout'
import { definitiveFailure } from './readings'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { overlayExitMs } from '../../utils/animation'
import { actorRowResource } from './SubstrateActors'
import type { OpenSubject } from './SubstrateAttentionList'
import { ActorReadFailure } from './SubstrateActorPage'
import { SubstrateActorOverview } from './SubstrateActorOverview'
import { ActorStateBadge, Term } from './shared'

/**
 * An actor's drawer: the Overview of its page, with Open full view. Actors
 * are not Kubernetes objects, so they have this drawer rather than the
 * resource drawer, which Kubernetes objects opened from here still use.
 */
export function SubstrateActorDrawer({
  actor,
  onClose,
  onOpenFull,
  onOpenSubject,
}: {
  actor: SubstrateRef | null
  onClose: () => void
  onOpenFull: (actor: SubstrateRef, tab?: string) => void
  onOpenSubject: OpenSubject
}) {
  const presence = useAnimatedUnmount(!!actor, overlayExitMs('drawer'))
  useEffect(() => {
    if (!actor) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !e.defaultPrevented) onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [actor, onClose])
  if (!presence.shouldRender || !actor) return null
  // The resource drawer's frame (position, width, resizing), with an actor's content.
  return (
    <ResourceDetailDrawer resource={actorRowResource(actor)} isOpen={presence.isOpen} onClose={onClose}>
      {() => <ActorDrawerBody key={`${actor.atespace}/${actor.name}`} actor={actor} onClose={onClose} onOpenFull={onOpenFull} onOpenSubject={onOpenSubject} />}
    </ResourceDetailDrawer>
  )
}

function ActorDrawerBody({ actor, onClose, onOpenFull, onOpenSubject }: { actor: SubstrateRef; onClose: () => void; onOpenFull: (actor: SubstrateRef, tab?: string) => void; onOpenSubject: OpenSubject }) {
  const query = useSubstrateActor(actor)
  const detail = definitiveFailure(substrateErrorCode(query.error)) ? undefined : query.data
  return (
    <div role="dialog" aria-label={`Actor ${actor.name}`} className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-start gap-3 border-b border-theme-border px-4 py-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            {detail && <ActorStateBadge state={detail.actor.state} impact={detail.row.workerImpact} updateTime={detail.actor.updateTime} />}
          </div>
          <h2 className="mt-1 truncate font-mono text-base font-semibold text-theme-text-primary">{actor.name}</h2>
          <div className="text-xs text-theme-text-tertiary"><Term term="atespace">Atespace</Term>: <span className="font-mono">{actor.atespace}</span></div>
        </div>
        <Tooltip content="Close (Esc)" delay={150} position="bottom">
          <button type="button" aria-label="Close" onClick={onClose} className="rounded-lg p-1.5 text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary">
            <X className="h-4 w-4" />
          </button>
        </Tooltip>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4">
        <RefreshFailedNotice queries={[query]} className="mb-3" />
        {!detail && query.isLoading ? (
          <PaneLoader label={`Loading ${actor.name}…`} />
        ) : !detail ? (
          <ActorReadFailure actor={actor} error={query.error} onBack={onClose} backLabel="Close" />
        ) : (
          <SubstrateActorOverview detail={detail} framed={false} onOpenSubject={onOpenSubject}
            onShowLogs={() => onOpenFull(actor, 'logs')} onShowSnapshots={() => onOpenFull(actor, 'snapshots')} onShowEgress={() => onOpenFull(actor, 'egress')}
            actions={<div className="flex flex-wrap gap-2">
              <button type="button" className="btn-brand px-3 py-1 text-sm" onClick={() => onOpenFull(actor)}>Open actor</button>
              <button type="button" className="btn-secondary px-3 py-1 text-sm" onClick={() => onOpenFull(actor, 'snapshots')}>Snapshots</button>
              <button type="button" className="btn-secondary px-3 py-1 text-sm" onClick={() => onOpenFull(actor, 'logs')}>Logs</button>
            </div>}
          />
        )}
      </div>
    </div>
  )
}
