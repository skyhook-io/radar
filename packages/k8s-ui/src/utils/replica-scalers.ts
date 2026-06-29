import type { ResourceRef } from '../types'

export function replicaScalers(scalers?: ResourceRef[]): ResourceRef[] | undefined {
  const result = scalers?.filter((ref) => {
    const kind = ref.kind.toLowerCase()
    return kind === 'horizontalpodautoscaler' || kind === 'scaledobject'
  })
  return result && result.length > 0 ? result : undefined
}
