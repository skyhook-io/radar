export const CNPG_BARMAN_PLUGIN_NAME = 'barman-cloud.cloudnative-pg.io'

interface BackupCluster {
  metadata?: { name?: string }
  spec?: {
    backup?: { barmanObjectStore?: { destinationPath?: string }; volumeSnapshot?: unknown }
    plugins?: { name: string; enabled?: boolean; isWALArchiver?: boolean; parameters?: { barmanObjectName?: string; serverName?: string } }[]
  }
}

export interface CNPGBackupPlugin {
  name: string
  enabled: boolean
  isWALArchiver: boolean
  barmanObjectName?: string
  serverName?: string
}

export interface CNPGBackupDeclaration {
  plugins: CNPGBackupPlugin[]
  inTreeConfigured: boolean
  inTreeDestination?: string
  snapshotsConfigured: boolean
}

export function cnpgBackupDeclaration(cluster: BackupCluster): CNPGBackupDeclaration {
  const backup = cluster.spec?.backup
  return {
    inTreeConfigured: !!backup?.barmanObjectStore,
    inTreeDestination: backup?.barmanObjectStore?.destinationPath,
    snapshotsConfigured: backup?.volumeSnapshot != null,
    plugins: (cluster.spec?.plugins ?? []).filter((p) => p?.name).map((p) => ({
      name: p.name,
      enabled: p.enabled !== false,
      isWALArchiver: p.isWALArchiver === true,
      barmanObjectName: p.parameters?.barmanObjectName,
      serverName: p.parameters?.serverName || cluster.metadata?.name,
    })),
  }
}

export function cnpgBarmanPlugin(declaration: CNPGBackupDeclaration): CNPGBackupPlugin | undefined {
  return declaration.plugins.find((p) => p.enabled && p.name === CNPG_BARMAN_PLUGIN_NAME)
}

export function cnpgDestinationPlugin(declaration: CNPGBackupDeclaration): CNPGBackupPlugin | undefined {
  return declaration.plugins.find((p) => p.enabled && (p.name !== CNPG_BARMAN_PLUGIN_NAME || !!p.barmanObjectName))
}

export function cnpgHasBackupDestination(declaration: CNPGBackupDeclaration): boolean {
  return !!declaration.inTreeDestination || declaration.snapshotsConfigured || !!cnpgDestinationPlugin(declaration)
}

export interface CNPGBackupBlocker {
  code: 'no_destination' | 'method_destination_missing'
  method: string
  plugin?: string
}

export function cnpgBackupDestinationBlocker(declaration: CNPGBackupDeclaration, method = 'barmanObjectStore', plugin?: string): CNPGBackupBlocker | null {
  const blocked = method === 'barmanObjectStore' ? !declaration.inTreeDestination
    : method === 'volumeSnapshot' ? !declaration.snapshotsConfigured
    : method === 'plugin' ? !declaration.plugins.some((p) => p.name === plugin && p.enabled && (p.name !== CNPG_BARMAN_PLUGIN_NAME || !!p.barmanObjectName))
    : false
  return blocked ? { code: cnpgHasBackupDestination(declaration) ? 'method_destination_missing' : 'no_destination', method, plugin: cnpgDestinationPlugin(declaration)?.name } : null
}

export function cnpgBackupBlockerText(blocker: CNPGBackupBlocker): string {
  return blocker.code === 'no_destination' ? 'No backup destination' : `No ${blocker.method} destination`
}
