import { useState } from 'react'
import { Copy, RefreshCw, Terminal } from 'lucide-react'
import { formatAge, getRunningEphemeralContainers, useOpenTerminal } from '@skyhook-io/k8s-ui'
import { copyText } from '@skyhook-io/k8s-ui/utils/clipboard'
import { useResource } from '../../api/client'
import { connectionHeaderValue, useCurrentConnection } from '../../api/connectionBinding'
import { getApiBase } from '../../api/config'
import { usePodDebugAccess } from '../../api/podDebug'
import { useNavCustomization } from '../../context/NavCustomization'
import { useCapabilitiesContext, useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { PodDebugAction, runningPodDebugTargets, type PodDebugTarget } from '../resources/PodDebugAction'

export function MissingShellDebug({ namespace, name, uid, target, connection, base }: {
  namespace: string; name: string; uid: string; target: string; connection?: string; base: string
}) {
  const { embedded } = useNavCustomization()
  const { username, authEnabled, deployment } = useCapabilitiesContext()
  const { podDebugImage, canOpenEphemeralTerminal } = useNamespacedCapabilities(namespace)
  const execOnly = !!embedded || deployment?.mode === 'cloud'
  const current = () => getApiBase() === base && connectionHeaderValue() === connection
  const currentConnection = useCurrentConnection()
  const sameCluster = currentConnection === connection && getApiBase() === base
  const enabled = sameCluster
  const podQuery = useResource<PodDebugTarget>('pods', namespace, name, undefined, { enabled })
  const pod = podQuery.data
  const usable = enabled && !podQuery.error && pod?.metadata.uid === uid && runningPodDebugTargets(pod)?.includes(target)
  const running = usable ? getRunningEphemeralContainers(pod).filter(c => c.targetContainerName === target) : []
  const { query, support, recheckSupport } = usePodDebugAccess(namespace, name, uid, !!usable && (execOnly ? !!running.length && !canOpenEphemeralTerminal : !running.length && !podDebugImage), execOnly, usable && !execOnly && !running.length ? podDebugImage : undefined)
  const openTerminal = useOpenTerminal()
  const [choice, setChoice] = useState('')
  const [copied, setCopied] = useState<'copied' | 'manual'>()
  if (!sameCluster) return <p className="text-xs text-theme-text-secondary">The cluster connection changed. Open the Pod on the current cluster to start a terminal.</p>
  if (podQuery.error) return <div className="space-y-2 text-xs text-theme-text-secondary">
    <p>Radar couldn’t read the Pod to check its debug options.</p>
    <button className="btn-secondary px-2 py-1" onClick={() => void podQuery.refetch()}>Check Pod again</button>
  </div>
  if (!pod) return <p className="text-xs text-theme-text-secondary">Checking debug options…</p>
  if (pod.metadata.uid !== uid) return <p className="text-xs text-theme-text-secondary">This Pod was replaced. Open its details to start a new terminal.</p>
  if (!usable) return <p className="max-w-md text-xs text-theme-text-secondary">Debug recovery needs a running Linux container. Check the container state in Pod details.</p>
  if (running.length && support === 'supported' && (!execOnly || canOpenEphemeralTerminal || (!query.error && query.data?.exec))) {
    const selected = running.find(c => c.name === choice) ?? running[0]
    return <div className="flex w-full min-w-0 max-w-md flex-col items-center gap-2">
      <p className="text-xs text-theme-text-secondary">{running.length === 1 ? 'A debug container is already running' : `${running.length} debug containers are running`} for <code>{target}</code>.</p>
      {running.length > 1 && <select aria-label="Running debug container" className="w-full min-w-0 max-w-full rounded border border-theme-border bg-theme-surface px-2 py-1 text-xs"
        value={selected.name} onChange={event => setChoice(event.target.value)}>
        {running.map(c => <option key={c.name} value={c.name}>{c.startedAt && `${formatAge(c.startedAt)} ago · `}{c.name} · {c.image}</option>)}
      </select>}
      <div className="max-w-full text-xs text-theme-text-tertiary [overflow-wrap:anywhere]">
        <div><code>{selected.name}</code></div>
        <div>{selected.image}{selected.startedAt && <> · started {formatAge(selected.startedAt)} ago</>}</div>
      </div>
      <button className="btn-brand flex items-center gap-2 rounded px-3 py-1.5 text-xs" onClick={() => {
        if (current()) openTerminal({ namespace, podName: name, containerName: selected.name, containers: [selected.name],
          debugSession: { podUID: uid, image: selected.image, connectionId: connection, apiBase: base } })
      }}><Terminal className="h-3.5 w-3.5"/>Open debug terminal</button>
    </div>
  }
  if (execOnly) {
    if (running.length && support === 'unknown') return <div className="flex flex-col items-center gap-2 text-xs text-theme-text-secondary">
      <p>Radar hasn’t confirmed whether this cluster can reopen the running debugger.</p>
      <button className="btn-secondary rounded px-2 py-1" onClick={() => void recheckSupport()}>Recheck support</button>
    </div>
    if (running.length && support === 'supported' && !canOpenEphemeralTerminal) {
      return <div className="flex flex-col items-center gap-2 text-xs text-theme-text-secondary">
        <p>{query.isFetching ? 'Checking whether you can open this debugger…' : query.data?.state === 'unavailable' && !query.error ? 'A debugger is already running. Ask your administrator to check your Pod exec access and whether this Radar allows exec.' : 'Radar couldn’t verify terminal access to this running debugger.'}</p>
        {!query.isFetching && <button className="btn-secondary rounded px-2 py-1" onClick={() => void query.refetch()}>Recheck access</button>}
      </div>
    }
    return <p className="max-w-md text-xs text-theme-text-secondary">To start a debug container, run <code>kubectl debug</code> with an identity permitted to update this Pod, or open the Pod in standalone Radar.</p>
  }
  const access = query.error ? undefined : query.data
  const allowedImage = access?.state === 'allowed' ? access.image :
    access?.state === 'denied' || access?.state === 'unavailable' ? undefined : podDebugImage
  if (allowedImage) return <div className="flex flex-col items-center gap-2">
    <p className="text-xs text-theme-text-secondary">Open a shell in a separate container in this Pod.</p>
    <PodDebugAction pod={pod} defaultImage={allowedImage} failedContainer={target}/>
  </div>
  if (support !== 'supported') {
    return support === 'unsupported' ? <p className="max-w-sm text-xs text-theme-text-secondary">Upgrade Radar to check debug access for this Pod, or ask your cluster administrator to help start a debug container.</p> : <div className="flex flex-col items-center gap-2 text-xs text-theme-text-secondary">
      <p>Radar hasn’t confirmed debug support for this cluster.</p>
      <button className="btn-secondary rounded px-2 py-1" onClick={() => void recheckSupport()}>Recheck support</button>
    </div>
  }
  if (query.isPending && !query.error) return <p className="text-xs text-theme-text-secondary">Checking access for this Pod…</p>
  if (access?.state === 'unavailable') return <div className="flex max-w-md flex-col items-center gap-2 text-xs text-theme-text-secondary">
    <p>A debug terminal is unavailable for this Pod. Ask your administrator to check Pod exec access and whether exec is enabled on this Radar.</p>
    <button className="btn-secondary rounded px-2 py-1" disabled={query.isFetching} onClick={() => void query.refetch()}>Recheck access</button>
  </div>
  const denied = access?.state === 'denied'
  const sharedServiceAccount = !username && !authEnabled && deployment?.mode === 'in-cluster'
  const requester = username ? `Kubernetes user ${username}` : sharedServiceAccount ? 'the ServiceAccount used by this Radar installation' : 'me'
  const subjectDetails = sharedServiceAccount ? ' For the ServiceAccount subject, use the running Radar Pod’s namespace and its spec.serviceAccountName.' : ''
  const request = denied ? `Could you grant ${requester} permission to update pods/ephemeralcontainers for Pod ${name} in namespace ${namespace}? I need to troubleshoot container ${target}. Please limit the rule to resourceNames: ["${name}"]. A replacement Pod with another name would need its own grant.${subjectDetails}`
    : `Could you check whether ${username ? `Kubernetes user ${username}` : sharedServiceAccount ? requester : 'my Kubernetes identity'} can update pods/ephemeralcontainers for Pod ${name} in namespace ${namespace}? Radar couldn’t verify access while I was troubleshooting container ${target}.${subjectDetails}`
  return <div className="flex max-w-md flex-col items-center gap-2 text-xs text-theme-text-secondary">
    <p>{denied ? <>{sharedServiceAccount ? "Radar’s ServiceAccount doesn’t have" : "You don’t have"} permission to add a debug container to this Pod. Copy an access request for your cluster administrator.</>
      : <>Radar couldn’t verify whether you can add a debug container to <code>{namespace}/{name}</code>. Recheck access or ask your administrator to verify it.</>}</p>
    {denied && <p className="text-xs text-theme-text-tertiary">Pod <code>{namespace}/{name}</code></p>}
    <div className="flex flex-wrap items-center justify-center gap-2">
      <button className="btn-secondary flex items-center gap-1.5 rounded px-2 py-1" onClick={async () => setCopied(await copyText(request) ? 'copied' : 'manual')}>
        <Copy className="h-3 w-3"/>{copied === 'copied' ? 'Copied' : denied ? 'Copy access request' : 'Copy verification request'}</button>
      <button className="btn-secondary flex items-center gap-1.5 rounded px-2 py-1" disabled={query.isFetching}
        onClick={() => { setCopied(undefined); void query.refetch() }}><RefreshCw className="h-3 w-3"/>{query.isFetching ? 'Checking…' : 'Recheck access'}</button>
    </div>
    {copied === 'manual' && <textarea aria-label="Access request to copy" className="w-full rounded bg-theme-elevated p-2" value={request} readOnly onFocus={event => event.target.select()}/>}
  </div>
}
