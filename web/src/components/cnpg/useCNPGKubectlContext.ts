import { useCapabilities, useContexts } from '../../api/client'
import { useConnection } from '../../context/ConnectionContext'
import { useNavCustomization } from '../../context/NavCustomization'

export function useCNPGKubectlContext(): { name: string; source?: string } | undefined {
  const { connection } = useConnection()
  const { data } = useCapabilities()
  const { data: contexts } = useContexts()
  const { embedded } = useNavCustomization()
  if (embedded || data?.deployment?.mode !== 'local') return undefined
  const context = contexts?.find((context) => context.name === connection.context)
  return context ? { name: context.originalName || context.name, source: context.source } : undefined
}
