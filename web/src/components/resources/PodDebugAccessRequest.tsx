import { useState } from 'react'
import { Copy, RefreshCw } from 'lucide-react'
import { copyText } from '@skyhook-io/k8s-ui/utils/clipboard'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'

export function PodDebugAccessRequest({ namespace, name, target, denied, checking, onRecheck }: {
  namespace: string; name: string; target: string; denied: boolean; checking?: boolean; onRecheck?: () => void
}) {
  const { username, authEnabled, deployment } = useCapabilitiesContext()
  const sharedServiceAccount = !username && !authEnabled && deployment?.mode === 'in-cluster'
  const [copied, setCopied] = useState<'copied' | 'manual'>()
  const requester = username ? `Kubernetes user ${username}` : sharedServiceAccount ? 'the ServiceAccount used by this Radar installation' : 'me'
  const subjectDetails = sharedServiceAccount ? ' Anyone who can open this Radar uses that permission. For the ServiceAccount subject, use the running Radar Pod’s namespace and its spec.serviceAccountName.'
    : !username && deployment?.mode === 'local' ? ' I can identify my Kubernetes user with kubectl auth whoami in this context.' : ''
  const request = denied ? `Could you grant ${requester} permission to update pods/ephemeralcontainers for Pod ${name} in namespace ${namespace}? I need to troubleshoot container ${target}. Please limit the rule to resourceNames: ["${name}"]. A replacement Pod with another name would need its own grant.${subjectDetails}`
    : `Could you check whether ${username ? `Kubernetes user ${username}` : sharedServiceAccount ? requester : 'my Kubernetes identity'} can update pods/ephemeralcontainers for Pod ${name} in namespace ${namespace}? Radar couldn’t verify access while I was troubleshooting container ${target}.${subjectDetails}`
  return <>
    <div className="flex flex-wrap items-center justify-center gap-2">
      <button className="btn-secondary flex items-center gap-1.5 rounded px-2 py-1" onClick={async () => setCopied(await copyText(request) ? 'copied' : 'manual')}>
        <Copy className="h-3 w-3"/>{copied === 'copied' ? 'Copied' : denied ? 'Copy access request' : 'Copy verification request'}</button>
      {onRecheck && <button className="btn-secondary flex items-center gap-1.5 rounded px-2 py-1" disabled={checking}
        onClick={() => { setCopied(undefined); onRecheck() }}><RefreshCw className="h-3 w-3"/>{checking ? 'Checking…' : 'Recheck access'}</button>}
    </div>
    {copied === 'manual' && <textarea aria-label={denied ? 'Access request to copy' : 'Verification request to copy'} className="w-full rounded bg-theme-elevated p-2" value={request} readOnly onFocus={event => event.target.select()}/>}
  </>
}
