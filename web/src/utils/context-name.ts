import { parseContextName } from '@skyhook-io/k8s-ui/utils/context-name'
import type { ContextInfo } from '../types'

// Re-export from the shared @skyhook-io/k8s-ui package.
export * from '@skyhook-io/k8s-ui/utils/context-name'

export function parseContextForSwitcher(context: ContextInfo) {
  const raw = context.originalName || context.name
  const nameQualifier = context.originalName && context.name !== context.originalName
    ? context.name.slice(context.originalName.length).trim() || undefined
    : undefined
  return {
    ...parseContextName(raw),
    nameQualifier,
  }
}

export function visibleContextQualifier(
  qualifier: string | undefined,
  source: string | undefined,
  sourceLabelVisible: boolean,
) {
  return sourceLabelVisible && qualifier === `(${source})` ? undefined : qualifier
}
