// What resolves each read code, mirroring Radar's pkg/readreason. A test
// checks this table against pkg/readreason/testdata/actions.json.
export type ReadAction =
  | 'retry'
  | 'wait'
  | 'check_target'
  | 'change_request'
  | 'user_access'
  | 'radar_config'
  | 'none'

export const READ_ACTIONS: Record<string, ReadAction> = {
  cluster_connecting: 'retry',
  cluster_disconnected: 'radar_config',
  connection_changed: 'check_target',
  filter_eval_failed: 'change_request',
  kind_not_watchable: 'change_request',
  kind_sync_pending: 'retry',
  kind_sync_timed_out: 'wait',
  kind_unknown: 'change_request',
  list_too_large: 'change_request',
  namespace_not_found: 'change_request',
  namespace_required: 'change_request',
  object_not_found: 'none',
  outside_namespace_pin: 'radar_config',
  outside_namespace_selection: 'change_request',
  radar_credentials_rejected: 'radar_config',
  radar_forbidden: 'radar_config',
  radar_kind_not_collected: 'radar_config',
  radar_namespace_not_collected: 'radar_config',
  read_error: 'retry',
  user_forbidden: 'user_access',
  user_namespace_not_visible: 'change_request',
  user_no_namespace_access: 'user_access',
  user_permission_check_failed: 'retry',
}

// Spellings Radar releases sent before the shared vocabulary, for codes that
// kept their meaning: a newer UI reading an older Radar reads them as the code
// they became.
const LEGACY_READ_CODES: Record<string, string> = {
  kind_sync_failed: 'kind_sync_timed_out',
  rbac_denied: 'user_forbidden',
}

/** The current spelling of a read code, translating one an older Radar sent. */
export function canonicalReadCode(code: unknown): unknown {
  return typeof code === 'string' ? (LEGACY_READ_CODES[code] ?? code) : code
}

/** The action for a code; an unknown code (from a newer Radar) is treated as needing a change. */
export function readActionFor(code: unknown): ReadAction | undefined {
  const current = canonicalReadCode(code)
  return typeof current === 'string' ? (READ_ACTIONS[current] ?? 'change_request') : undefined
}
