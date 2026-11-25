import { connectionHeaderValue } from '../../api/connectionBinding'
import { useConnection } from '../../context/ConnectionContext'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'

export function usePodDebugContext() {
  const { connection } = useConnection()
  if (connection.connectionId !== connectionHeaderValue()) return undefined
  return connection.context === 'in-cluster' ? connection.clusterName || connection.context : connection.context
}

export function PodDebugPermissionScope({ pod }: { pod?: { namespace: string; name: string } }) {
  return <><code className="inline-code">update pods/ephemeralcontainers</code> for {pod
    ? <>Pod <code className="inline-code break-all">{pod.namespace}/{pod.name}</code></>
    : 'this Pod'}</>
}

export function PodDebugPermission({ namespace, name }: { namespace: string; name: string }) {
  const { username, authEnabled, deployment } = useCapabilitiesContext()
  const sharedServiceAccount = !username && !authEnabled && deployment?.mode === 'in-cluster'
  return <>Ask your cluster admin {sharedServiceAccount ? 'to grant Radar’s ServiceAccount access' : 'for access'} to <PodDebugPermissionScope pod={{ namespace, name }} />.</>
}
