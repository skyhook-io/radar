import { useState } from 'react'
import { Copy, RefreshCw, Terminal } from 'lucide-react'
import { useOpenTerminal } from '@skyhook-io/k8s-ui'
import { copyText } from '@skyhook-io/k8s-ui/utils/clipboard'
import { useResource } from '../../api/client'
import { connectionHeaderValue } from '../../api/connectionBinding'
import { getApiBase } from '../../api/config'
import { usePodDebugAccess } from '../../api/podDebug'
import { useNavCustomization } from '../../context/NavCustomization'
import { useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { PodDebugAction, runningPodDebugTargets, type PodDebugTarget } from '../resources/PodDebugAction'

export function MissingShellDebug({ namespace, name, uid, target, connection, base }: {
  namespace: string; name: string; uid: string; target: string; connection?: string; base: string
}) {
  const { embedded } = useNavCustomization()
  const { podDebugImage } = useNamespacedCapabilities(namespace)
  const current = () => getApiBase() === base && connectionHeaderValue() === connection
  const enabled = !embedded && current()
  const podQuery = useResource<PodDebugTarget>('pods', namespace, name, undefined, { enabled })
  const pod = podQuery.data
  const usable = enabled && !podQuery.error && pod?.metadata.uid === uid && runningPodDebugTargets(pod)?.includes(target)
  const running = usable ? (pod.spec.ephemeralContainers ?? []).filter(c => c.targetContainerName === target &&
    pod.status.ephemeralContainerStatuses?.some(status => status.name === c.name && status.state?.running)) : []
  const { query, support } = usePodDebugAccess(namespace, name, uid, !!usable && !running.length)
  const openTerminal = useOpenTerminal()
  const [choice, setChoice] = useState('')
  const [copied, setCopied] = useState<'copied' | 'manual'>()
  if (!enabled) return null
  if (podQuery.error) return <div className="space-y-2 text-xs text-theme-text-secondary">
    <p>Radar couldn’t read the Pod to check its debug options.</p>
    <button className="btn-secondary px-2 py-1" onClick={() => void podQuery.refetch()}>Check Pod again</button>
  </div>
  if (!pod) return <p className="text-xs text-theme-text-secondary">Checking debug options…</p>
  if (pod.metadata.uid !== uid) return <p className="text-xs text-theme-text-secondary">This Pod was replaced. Open its details to start a new terminal.</p>
  if (!usable) return null
  if (running.length && support === 'supported') {
    const selected = running.find(c => c.name === choice) ?? running[0]
    return <div className="flex flex-col items-center gap-2">
      <p className="text-xs text-theme-text-secondary">A debug container is already running for <code>{target}</code>.</p>
      {running.length === 1 && <p className="max-w-sm break-all text-xs text-theme-text-tertiary"><code>{selected.name}</code> · {selected.image}</p>}
      {running.length > 1 && <select aria-label="Running debug container" className="rounded border border-theme-border bg-theme-surface px-2 py-1 text-xs"
        value={selected.name} onChange={event => setChoice(event.target.value)}>
        {running.map(c => <option key={c.name} value={c.name}>{c.name} · {c.image}</option>)}
      </select>}
      <button className="btn-brand flex items-center gap-2 rounded px-3 py-1.5 text-xs" onClick={() => {
        if (current()) openTerminal({ namespace, podName: name, containerName: selected.name, containers: [selected.name],
          debugSession: { podUID: uid, image: selected.image, connectionId: connection, apiBase: base } })
      }}><Terminal className="h-3.5 w-3.5"/>Open debug terminal</button>
    </div>
  }
  if (support !== 'supported') {
    if (podDebugImage) return <PodDebugAction pod={pod} defaultImage={podDebugImage} failedContainer={target}/>
    return support === 'unsupported' ? <p className="max-w-sm text-xs text-theme-text-secondary">This version of Radar can’t check debug access for this Pod.</p> : null
  }
  if (query.isPending && !query.error) return <p className="text-xs text-theme-text-secondary">Checking access for this Pod…</p>
  const access = query.error ? undefined : query.data
  if (access?.state === 'allowed') return <PodDebugAction pod={pod} defaultImage={access.image} failedContainer={target}/>
  if (access?.state === 'unavailable') return null
  const denied = access?.state === 'denied'
  const request = `Could you grant me permission to update pods/ephemeralcontainers for Pod ${name} in namespace ${namespace}? I need to add a debug container to troubleshoot ${target}.`
  return <div className="flex max-w-md flex-col items-center gap-2 text-xs text-theme-text-secondary">
    <p>{denied ? <>You don’t have permission to add a debug container to this Pod. Copy an access request for your cluster administrator.</>
      : <>Radar couldn’t verify whether you can add a debug container to <code>{namespace}/{name}</code>.</>}</p>
    <div className="flex flex-wrap items-center justify-center gap-2">
      {denied && <button className="btn-secondary flex items-center gap-1.5 rounded px-2 py-1" onClick={async () => setCopied(await copyText(request) ? 'copied' : 'manual')}>
        <Copy className="h-3 w-3"/>{copied === 'copied' ? 'Copied' : 'Copy access request'}</button>}
      <button className="btn-secondary flex items-center gap-1.5 rounded px-2 py-1" disabled={query.isFetching}
        onClick={() => { setCopied(undefined); void query.refetch() }}><RefreshCw className="h-3 w-3"/>{query.isFetching ? 'Checking…' : 'Recheck access'}</button>
    </div>
    {denied && copied === 'manual' && <textarea aria-label="Access request to copy" className="w-full rounded bg-theme-elevated p-2" value={request} readOnly onFocus={event => event.target.select()}/>}
  </div>
}
