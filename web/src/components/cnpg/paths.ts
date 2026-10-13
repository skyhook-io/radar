import { cnpgDetailPath } from './routes'

export function cnpgClusterFullPath(namespace: string, name: string, ctx?: string, tab?: string): string {
  return cnpgDetailPath({ plural: 'clusters', namespace, name }, ctx, tab)
}

export { currentPageLabel } from '../../utils/page-links'
