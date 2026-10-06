import { useCapabilities } from '../../api/client'
import { useConnection } from '../../context/ConnectionContext'
import { useNavCustomization } from '../../context/NavCustomization'

export function useCNPGKubectlContext(): string | undefined {
  const { connection } = useConnection()
  const { data } = useCapabilities()
  const { embedded } = useNavCustomization()
  return !embedded && data?.deployment?.mode === 'local' ? connection.context || undefined : undefined
}
