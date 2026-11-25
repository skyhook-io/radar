import { connectionHeaderValue } from '../../api/connectionBinding'
import { useConnection } from '../../context/ConnectionContext'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'

export function usePodDebugContext() {
  const { connection } = useConnection()
  if (connection.connectionId !== connectionHeaderValue()) return undefined
  return connection.context === 'in-cluster' ? connection.clusterName || connection.context : connection.context
}

export function PodDebugPermission({ namespace, name }: { namespace: string; name: string }) {
  const { username, authEnabled, deployment } = useCapabilitiesContext()
  const sharedServiceAccount = !username && !authEnabled && deployment?.mode === 'in-cluster'
  return <>Ask your cluster admin {sharedServiceAccount ? 'to grant Radar’s ServiceAccount access' : 'for access'} to <code>update</code> <code>pods/ephemeralcontainers</code> for Pod <code className="break-all">{namespace}/{name}</code>.</>
}
