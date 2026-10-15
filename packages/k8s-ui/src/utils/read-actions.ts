// What resolves each read code, mirroring Radar's pkg/readreason. A test
// checks this table against pkg/readreason/testdata/actions.json.
export type ReadAction =
  | 'retry'
  | 'wait'
  | 'request_kind'
  | 'check_target'
  | 'change_request'
  | 'caller_access'
  | 'collector_config'
  | 'none'

export const READ_ACTIONS: Record<string, ReadAction> = {
  cluster_connecting: 'retry',
  cluster_disconnected: 'collector_config',
  collector_credentials_rejected: 'collector_config',
  collector_forbidden: 'collector_config',
  collector_scope_limited: 'collector_config',
  connection_changed: 'check_target',
  filter_eval_failed: 'change_request',
  kind_not_watchable: 'change_request',
  kind_not_watched: 'collector_config',
  kind_sync_failed: 'wait',
  kind_sync_pending: 'retry',
  kind_unknown: 'change_request',
  kind_watch_failed: 'request_kind',
  list_too_large: 'change_request',
  namespace_not_found: 'change_request',
  namespace_not_visible: 'change_request',
  namespace_required: 'change_request',
  no_namespace_access: 'caller_access',
  object_not_found: 'none',
  outside_namespace_scope: 'change_request',
  outside_namespace_selection: 'change_request',
  permission_check_failed: 'retry',
  rbac_denied: 'caller_access',
  read_error: 'retry',
}

/** The action for a code; an unknown code (from a newer Radar) is treated as needing a change. */
export function readActionFor(code: unknown): ReadAction | undefined {
  return typeof code === 'string' ? (READ_ACTIONS[code] ?? 'change_request') : undefined
}
