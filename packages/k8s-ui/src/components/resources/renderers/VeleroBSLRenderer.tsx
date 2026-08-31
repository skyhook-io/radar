import { HardDrive, Clock, Archive } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, RelationshipGroup, AlertBanner } from '../../ui/drawer-components'
import { TimeValue } from '../../ui/ScheduleValue'
import {
  getBSLStatus,
  getBSLProvider,
  getBSLBucket,
  getBSLPrefix,
  getBSLRegion,
  getBSLDefault,
  getBSLAccessMode,
  getBSLLastValidation,
  getBSLLastSynced,
  getBSLMessage,
} from '../resource-utils-velero'
import { VeleroPhaseValue } from './velero-cells'

/** Past its TTL.
 *
 *  Not the same as unrestorable: Velero's restore controller does not check
 *  expiration, so until the garbage collector creates the DeleteBackupRequest
 *  the data is still in the bucket and a restore from it would work. What is
 *  true is that Velero intends to delete it, on a schedule nothing here can
 *  see — so it is not something to plan a recovery around, and the wording
 *  below says that rather than claiming it is already gone. */
function veleroExpired(b: { expiration?: string }): boolean {
  if (!b.expiration) return false
  const at = Date.parse(b.expiration)
  return Number.isFinite(at) && at < Date.now()
}

/** One Backup held in this location, from the stored-backups lookup. */
export interface VeleroStoredBackup {
  namespace: string
  name: string
  phase?: string
  expiration?: string
  completed?: string
}

interface VeleroBSLRendererProps {
  data: any
  /**
   * What this location holds. Undefined means the lookup is unresolved — still
   * loading, failed, or not wired by this host — which is not the same as a
   * location holding nothing, and must not render as it.
   */
  storedBackups?: VeleroStoredBackup[]
  /** How many the location holds in total. Larger than the list when the server
   *  capped it — the counts describe the location, the list is a page of it. */
  storedTotal?: number
  /** How many of those are complete restore points, counted by the server over
   *  the whole location. Deriving this from the list instead would pair a page's
   *  count with the location's total and undercount a capped location. */
  restorableTotal?: number
  /** How many have aged out, on the same terms. */
  expiredTotal?: number
  /** The list is a page. Said out loud, because a capped list otherwise reads as
   *  everything this location holds. */
  listTruncated?: boolean
  /** Rendered by the host when the lookup failed, so a denial is not silence. */
  lookupNote?: React.ReactNode
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function VeleroBSLRenderer({ data, storedBackups, storedTotal, restorableTotal, expiredTotal, listTruncated, lookupNote, onNavigate }: VeleroBSLRendererProps) {
  const status = data.status || {}
  const conditions = status.conditions || []
  const bslStatus = getBSLStatus(data)
  const message = getBSLMessage(data)
  const config = data.spec?.config || {}
  // Two things falsify "restorable", and the phase sees neither. A backup that
  // did complete is still not something to go back to once its TTL has passed —
  // Velero deletes expired backups, and while its controller is down they sit
  // here looking finished long after the data behind them went.
  const usable = (storedBackups ?? []).filter((b) => b.phase === 'Completed' && !veleroExpired(b))
  // The server's counts when the host wired them, because they are the
  // location's; the page's own only as a fallback for a host that did not.
  // Mixing the two reads worst on exactly the locations that need this panel —
  // a location holding thousands is where a capped page and a full total sit in
  // the same sentence.
  const restorable = restorableTotal ?? usable.length
  const expired = expiredTotal ?? (storedBackups ?? []).filter((b) => b.phase === 'Completed' && veleroExpired(b)).length
  // The location's total, not the page's length: every sentence below counts
  // what the location holds, and the list may be capped.
  const stored = storedTotal ?? storedBackups?.length ?? 0
  const newest = usable
    .map((b) => b.completed)
    .filter((c): c is string => !!c)
    .sort()
    .pop()

  return (
    <>
      {/* The reason, not just the state. An unavailable location means Velero's
          last validation of the bucket failed, and status.message is the only
          place that reason exists — the object carries no conditions. */}
      {bslStatus.level === 'unhealthy' && (
        <AlertBanner
          variant="error"
          title="Storage Location Unavailable"
          message={message
            ? message
            : 'Velero reported this location as unavailable and gave no reason. New backups cannot be written here, and backups already stored here cannot be restored from, while it stays that way.'}
        />
      )}

      {/* Status section */}
      <Section title="Status" icon={Clock} defaultExpanded>
        <PropertyList>
          <Property label="Phase" value={
            <VeleroPhaseValue status={bslStatus} phase={status.phase || ''} />
          } />
          <Property label="Last Validation" value={getBSLLastValidation(data)} />
          <Property label="Last Synced" value={getBSLLastSynced(data)} />
          {status.lastSyncedRevision && (
            <Property label="Last Synced Revision" value={
              <span className="text-sm font-mono text-theme-text-secondary break-all">{status.lastSyncedRevision}</span>
            } />
          )}
        </PropertyList>
      </Section>

      {/* Provider section */}
      <Section title="Provider" icon={HardDrive} defaultExpanded>
        <PropertyList>
          <Property label="Provider" value={getBSLProvider(data)} />
          <Property label="Bucket" value={getBSLBucket(data)} />
          <Property label="Prefix" value={getBSLPrefix(data)} />
          <Property label="Region" value={getBSLRegion(data)} />
          <Property label="Access Mode" value={getBSLAccessMode(data)} />
          <Property label="Default" value={getBSLDefault(data) ? 'Yes' : 'No'} />
        </PropertyList>
        {Object.keys(config).length > 0 && (
          <div className="mt-2 pt-2 border-t border-theme-border">
            <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Config</div>
            <div className="flex flex-wrap gap-1">
              {Object.entries(config).map(([k, v]) => (
                <span key={k} className="badge-sm bg-theme-hover text-theme-text-secondary">
                  {k}: {String(v)}
                </span>
              ))}
            </div>
          </div>
        )}
      </Section>


      {/* What depends on this location.
          "Unavailable" is a fact about the bucket; the reason anyone opens this
          page is what it costs them, and that is the list of backups they cannot
          restore from while it stays that way. Velero keeps reporting those
          backups as Completed — accurately, they did complete — so the phase
          alone never shows the cost. */}
      {(storedBackups !== undefined || lookupNote) && (
        <Section title="Stored Here" icon={Archive} defaultExpanded={bslStatus.level !== 'healthy'}>
          {lookupNote}
          {storedBackups !== undefined && storedBackups.length === 0 && !lookupNote && (
            <div className="text-sm text-theme-text-tertiary">
              No backup in this namespace names this location.
            </div>
          )}
          {storedBackups !== undefined && storedBackups.length > 0 && (
            <>
              {/* Only when Velero actually said the location is unavailable.
                  `unknown` is the shape of a location Velero has not validated
                  yet — no phase at all — and reading that as "unreachable" turns
                  missing data into a statement about the bucket. */}
              {bslStatus.level === 'unhealthy' && restorable > 0 && (
                <div className="text-xs text-warning-text mb-2">
                  {/* Counts what `restorable` counts. "completed backups stored
                      here" described a larger set — expired backups completed
                      too — so the sentence and the number disagreed whenever a
                      location held both. */}
                  {`${restorable} ${restorable === 1 ? 'backup' : 'backups'} here ${restorable === 1 ? 'is' : 'are'} complete and still inside ${restorable === 1 ? 'its' : 'their'} retention. While this location is ${bslStatus.text}, ${restorable === 1 ? 'it is' : 'they are'} not something you can restore from.`}
                </div>
              )}
              {bslStatus.level === 'unknown' && restorable > 0 && (
                <div className="text-xs text-theme-text-secondary mb-2">
                  {`Velero has not reported a phase for this location, so whether ${restorable === 1 ? 'the backup still inside its retention' : `the ${restorable} backups still inside their retention`} can be restored from right now is not established here.`}
                </div>
              )}
                {/* Stands on its own. "Not counted above" referred to a line that
                    only renders for an unhealthy location, so on a healthy one —
                    and whenever every stored backup has expired — it pointed at
                    nothing. */}
              {expired > 0 && (
                <div className="text-xs text-theme-text-secondary mb-2">
                  {`${expired} stored ${expired === 1 ? 'backup has' : 'backups have'} passed ${expired === 1 ? 'its' : 'their'} retention. Velero deletes ${expired === 1 ? 'it' : 'them'} when its garbage collector next runs, so ${expired === 1 ? 'it is' : 'they are'} not something to plan a recovery around.`}
                  {restorable === 0 && ' Nothing stored here is still inside its retention.'}
                </div>
              )}
              {/* The list below is everything stored here, and its count is the
                  only number on a healthy location — so "Backups (2)" sat next
                  to a restorable point derived from one of them with nothing
                  reconciling the two.

                  Stated as the positive on purpose. The alternative — counting
                  the others and saying what they are — has to be right about
                  every phase in the enum, and the obvious phrasing is wrong
                  twice: a `Deleting` backup already ran, and the
                  *PartiallyFailed phases mean part of the data made it (see
                  BACKUP_PARTIAL_PHASES). Neither is "has not completed". How
                  many are whole restore points is true regardless. */}
              {restorable !== stored && (
                <div className="text-xs text-theme-text-secondary mb-2">
                  {restorable === 0
                    ? `${stored === 1 ? 'The backup stored here is not' : `None of the ${stored} backups stored here is`} a complete restore point.`
                    : `${restorable} of ${stored} backups stored here ${restorable === 1 ? 'is a complete restore point' : 'are complete restore points'}.`}
                </div>
              )}
              {listTruncated && (
                <div className="text-xs text-theme-text-tertiary mb-2">
                  {`Showing the ${storedBackups.length} most recent of ${stored}.`}
                </div>
              )}
              <RelationshipGroup
                label="Backups"
                refs={storedBackups.map((b) => ({
                  kind: 'Backup',
                  namespace: b.namespace,
                  name: b.name,
                  group: 'velero.io',
                }))}
                onNavigate={onNavigate}
              />
              {newest && (
                <div className="mt-2 pt-2 border-t border-theme-border text-xs text-theme-text-secondary">
                  {/* The question this page is opened to answer — worded for the
                      state the location is actually in. Calling this a
                      "restorable point" on an unavailable location contradicts
                      the sentence directly above it, which has just said these
                      are not something you can restore from. The date is still
                      worth having: it is what you get back when the location
                      recovers. */}
                  {bslStatus.level === 'healthy' ? (
                    <>
                      Most recent restorable point: <TimeValue timestamp={newest} /> ago
                    </>
                  ) : bslStatus.level === 'unhealthy' ? (
                    <>
                      Most recent point held here, once this location is reachable again:{' '}
                      <TimeValue timestamp={newest} /> ago
                    </>
                  ) : (
                    // Unknown: do not imply it is unreachable, and do not call it
                    // restorable either. Say what the timestamp is.
                    <>
                      Most recent point held here: <TimeValue timestamp={newest} /> ago
                    </>
                  )}
                </div>
              )}
            </>
          )}
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
