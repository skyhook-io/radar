import { canonicalReadCode, readActionFor } from '../../utils/read-actions'

/**
 * What Radar's read of one related object found, as the viewer. `not_found`
 * comes only from a read that could prove absence; a denial, a kind still
 * loading or any other failure is `unavailable` with its read code. A host
 * that doesn't read related objects passes no RelatedRead at all, and the
 * renderer shows names without read state.
 */
export type RelatedRead<T = any> =
  | { state: 'loading' }
  | { state: 'found'; object: T }
  | { state: 'not_found' }
  /** Not seen, and the read can't vouch for absence (an older Radar's list without coverage, or its uncoded 404). */
  | { state: 'unconfirmed' }
  | { state: 'unavailable'; code?: string; message?: string }

export function isDenied(read: RelatedRead | undefined): boolean {
  return read?.state === 'unavailable' && readActionFor(read.code) === 'user_access'
}

/** Why a related object couldn't be read, in a few words that fit "<name>: …". */
export function unreadReason(read: RelatedRead | undefined): string {
  if (!read || read.state === 'loading') return 'checking…'
  if (read.state === 'unconfirmed') return 'this Radar can’t confirm whether it exists'
  if (read.state !== 'unavailable') return ''
  if (isDenied(read)) return 'you can’t read it'
  switch (canonicalReadCode(read.code)) {
    case 'kind_sync_pending':
    case 'cluster_connecting':
      return 'Radar is still loading it'
    case 'kind_sync_timed_out':
      return 'Radar’s copy is overdue'
    case 'radar_forbidden':
    case 'radar_credentials_rejected':
      return 'Radar can’t read it'
    case 'radar_kind_not_collected':
    case 'radar_namespace_not_collected':
      return 'Radar doesn’t collect it'
    case 'connection_changed':
    case 'cluster_disconnected':
      return 'Radar’s connection changed'
    case 'kind_unknown':
      return 'Radar’s API discovery doesn’t list its kind'
    default:
      return 'the read failed'
  }
}
