import { isApiGroup } from '../resources/resource-utils-cnpg'

export const CNPG_BACKUP_RUN_WINDOW_MS = 7 * 24 * 60 * 60 * 1000

export function cnpgBackupRunInFlight(backup: any): boolean {
  return backup?.status?.phase !== 'completed' && backup?.status?.phase !== 'failed'
}

export function cnpgBackupRunTime(backup: any): string | undefined {
  return backup?.status?.stoppedAt || backup?.status?.startedAt || backup?.metadata?.creationTimestamp
}

export function cnpgBackupRunsInWindow(backups: any[], now = Date.now()): any[] {
  return backups.filter((b) => {
    if (!isApiGroup(b.apiVersion, 'postgresql.cnpg.io')) return false
    const time = Date.parse(cnpgBackupRunTime(b) ?? '')
    return cnpgBackupRunInFlight(b) || (Number.isFinite(time) && now - time <= CNPG_BACKUP_RUN_WINDOW_MS)
  }).sort((a, b) => Date.parse(cnpgBackupRunTime(b) ?? '') - Date.parse(cnpgBackupRunTime(a) ?? ''))
}
