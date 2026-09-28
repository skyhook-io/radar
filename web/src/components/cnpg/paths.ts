import { buildWorkloadPath } from '../../utils/navigation'

export function cnpgClusterFullPath(namespace: string, name: string): string {
  return buildWorkloadPath({ kind: 'clusters', namespace, name, group: 'postgresql.cnpg.io' })
}
