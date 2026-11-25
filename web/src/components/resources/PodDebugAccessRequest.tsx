import { useState } from 'react'
import { Copy, RefreshCw } from 'lucide-react'
import { copyText } from '@skyhook-io/k8s-ui/utils/clipboard'
import { connectionHeaderValue } from '../../api/connectionBinding'
import { useConnection } from '../../context/ConnectionContext'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'

export function usePodDebugContext() {
  const { connection } = useConnection()
  if (connection.connectionId !== connectionHeaderValue()) return undefined
  return connection.context === 'in-cluster' ? connection.clusterName || connection.context : connection.context
}

function usePodDebugRequester() {
  const { username, authEnabled, deployment } = useCapabilitiesContext()
  const sharedServiceAccount = !username && !authEnabled && deployment?.mode === 'in-cluster'
  const requester = username ? `Kubernetes user ${username}` : sharedServiceAccount ? 'the ServiceAccount used by this Radar installation' : 'me'
  const subjectDetails = sharedServiceAccount ? ' Anyone who can open this Radar uses that permission. For the ServiceAccount subject, use the running Radar Pod’s namespace and its spec.serviceAccountName.'
    : !username && deployment?.mode === 'local' ? ' I can identify my Kubernetes user with kubectl auth whoami in this context.' : ''
  return { username, sharedServiceAccount, requester, subjectDetails }
}

export function PodDebugDenialMessage() {
  const { sharedServiceAccount } = usePodDebugRequester()
  return <>{sharedServiceAccount ? "Radar’s ServiceAccount doesn’t have" : "You don’t have"} permission to add a debug container to this Pod.</>
}

export function PodDebugAccessRequest({ namespace, name, target, denied, checking, onRecheck, context }: {
  namespace: string; name: string; target: string; denied: boolean; context?: string; checking?: boolean; onRecheck?: () => void
}) {
  const { username, sharedServiceAccount, requester, subjectDetails } = usePodDebugRequester()
  const [copied, setCopied] = useState<'copied' | 'manual'>()
  const request = denied ? `Could you grant ${requester} permission to update pods/ephemeralcontainers for Pod ${name} in namespace ${namespace}? I need to troubleshoot container ${target}. Please limit the rule to resourceNames: ["${name}"]. A replacement Pod with another name would need its own grant.${subjectDetails}`
    : `Could you check whether ${username ? `Kubernetes user ${username}` : sharedServiceAccount ? requester : 'my Kubernetes identity'} can update pods/ephemeralcontainers for Pod ${name} in namespace ${namespace}? Radar couldn’t verify access while I was troubleshooting container ${target}.${subjectDetails}`
  return <>
    <div className="flex flex-wrap items-center justify-center gap-2">
      <button className="btn-secondary flex items-center gap-1.5 rounded px-2 py-1" onClick={async () => setCopied(await copyText(context ? `${request} Radar context: ${context}.` : request) ? 'copied' : 'manual')}>
        <Copy className="h-3 w-3"/>{copied === 'copied' ? 'Copied' : denied ? 'Copy access request' : 'Copy verification request'}</button>
      {onRecheck && <button className="btn-secondary flex items-center gap-1.5 rounded px-2 py-1" disabled={checking}
        onClick={() => { setCopied(undefined); onRecheck() }}><RefreshCw className="h-3 w-3"/>{checking ? 'Checking…' : 'Recheck access'}</button>}
    </div>
    {copied === 'manual' && <textarea aria-label={denied ? 'Access request to copy' : 'Verification request to copy'} className="w-full rounded bg-theme-elevated p-2" value={request} readOnly onFocus={event => event.target.select()}/>}
  </>
}
