import { cnpgDetailPath } from './routes'

export function cnpgClusterFullPath(namespace: string, name: string, ctx?: string, tab?: string): string {
  return cnpgDetailPath({ plural: 'clusters', namespace, name }, ctx, tab)
}

/**
 * The label for "← back" on the page a push lands on: the title of the page
 * being left, which Radar keeps in the document title.
 */
export function currentPageLabel(): string {
  return document.title.replace(/\s*·\s*Radar$/, '') || 'previous page'
}
