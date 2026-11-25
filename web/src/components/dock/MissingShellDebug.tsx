import { type ReactNode, useState } from 'react'
import { Terminal } from 'lucide-react'
import { formatAge, getRunningEphemeralContainers, useOpenTerminal } from '@skyhook-io/k8s-ui'
import { PodDebugAccessRequest } from '../resources/PodDebugAccessRequest'
import { useResource } from '../../api/client'
import { connectionHeaderValue, useCurrentConnectionId } from '../../api/connectionBinding'
import { getApiBase } from '../../api/config'
import { usePodDebugAccess } from '../../api/podDebug'
import { useNavCustomization } from '../../context/NavCustomization'
import { useCapabilitiesContext, useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { PodDebugAction, runningPodDebugTargets, type PodDebugTarget } from '../resources/PodDebugAction'

interface RecoveryTarget {
  namespace: string; name: string; uid: string; target: string; connection?: string; base: string; podDetailsLink?: ReactNode; switchContainer?: (name: string) => void
}

export function MissingShellDebug(props: RecoveryTarget) {
  const { namespace, name, uid, target, connection, base, podDetailsLink } = props
  const currentConnection = useCurrentConnectionId()
  const sameCluster = currentConnection === connection && getApiBase() === base
  const podQuery = useResource<PodDebugTarget>('pods', namespace, name, undefined, { enabled: sameCluster })
  const pod = podQuery.data
  if (!sameCluster) return <p className="text-xs text-theme-text-secondary">The cluster connection changed. Open the Pod on the current cluster to start a terminal.</p>
  if (podQuery.error) return <div className="space-y-2 text-xs text-theme-text-secondary">
    <p>Radar couldn’t read the Pod to check its debug options.</p>
    <button className="btn-secondary px-2 py-1" onClick={() => void podQuery.refetch()}>Check Pod again</button>
  </div>
  if (!pod) return <p className="text-xs text-theme-text-secondary">Checking debug options…</p>
  if (pod.metadata.uid !== uid) return <p className="text-xs text-theme-text-secondary">This Pod was replaced. Open its details to start a new terminal. {podDetailsLink}</p>
  const targets = runningPodDebugTargets(pod)
  if (!targets?.includes(target)) return <p className="max-w-md text-xs text-theme-text-secondary">Debug recovery needs a running Linux container. Check the container state in Pod details.</p>
  const untargeted = getRunningEphemeralContainers(pod).filter(c => !c.targetContainerName)
  const alternatives = targets.filter(name => name !== target)
  return <div className="flex w-full min-w-0 max-w-md flex-col items-center gap-2">
    <DebugOptions {...props} pod={pod}/>
    {untargeted.length > 0 && <p className="max-w-md text-xs text-theme-text-secondary [overflow-wrap:anywhere]">
      Running without a target: <code>{untargeted.map(c => c.name).join(', ')}</code>. For Pod-wide troubleshooting, check Pod details for terminal access to this debugger. {podDetailsLink}
    </p>}
    {alternatives.length > 0 && <p className="max-w-md text-xs text-theme-text-secondary [overflow-wrap:anywhere]">
      {alternatives.length === 1 ? 'Other running container' : 'Other running containers'}: <code>{alternatives.map((name, index) => <span key={name}>{index > 0 && ', '}{props.switchContainer
        ? <button type="button" className="underline underline-offset-2 hover:text-theme-text-primary" onClick={() => props.switchContainer?.(name)}>{name}</button>
        : name}</span>)}</code>
    </p>}
  </div>
}

function DebugOptions({ namespace, name, uid, target, connection, base, pod }: RecoveryTarget & {pod: PodDebugTarget}) {
  const { embedded } = useNavCustomization()
  const { username, authEnabled, deployment } = useCapabilitiesContext()
  const { podDebugImage, canOpenEphemeralTerminal } = useNamespacedCapabilities(namespace)
  const execOnly = !!embedded || deployment?.mode === 'cloud'
  const current = () => getApiBase() === base && connectionHeaderValue() === connection
  const running = getRunningEphemeralContainers(pod).filter(c => c.targetContainerName === target)
  const { query, support, recheckSupport, unverifiedImage, unverifiedReview, recheckAccess } = usePodDebugAccess(namespace, name, uid,
    execOnly ? !!running.length && !canOpenEphemeralTerminal : !running.length && !podDebugImage,
    execOnly, !execOnly && !running.length ? podDebugImage : undefined)
  const openTerminal = useOpenTerminal()
  const [choice, setChoice] = useState('')
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
    if (running.length && support === 'unsupported') return <p className="max-w-md text-xs text-theme-text-secondary">A debug container is already running. Upgrade this cluster’s Radar to reopen it here, or open a terminal to that container with kubectl.</p>
    return <p className="max-w-md text-xs text-theme-text-secondary">Run <code>kubectl debug</code> against this cluster with an approved image, or open this Pod in standalone Radar. Your kubectl identity needs permission to patch <code>pods/ephemeralcontainers</code>. <code className="mt-2 block select-all break-all rounded bg-theme-elevated p-2 text-left">kubectl --context CONTEXT -n {namespace} debug {name} --target={target} --image=APPROVED_IMAGE -it</code></p>
  }
  const access = query.error ? undefined : query.data
  const allowedImage = access?.state === 'allowed' ? access.image :
    access?.state === 'denied' || access?.state === 'unavailable' || access?.state === 'check_failed' ? undefined : podDebugImage
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
  return <div className="flex max-w-md flex-col items-center gap-2 text-xs text-theme-text-secondary">
    <p>{denied ? <>{sharedServiceAccount ? "Radar’s ServiceAccount doesn’t have" : "You don’t have"} permission to add a debug container to this Pod. Copy an access request for your cluster administrator.</>
      : access?.state === 'check_failed' ? <>You can open terminals in this Pod. Kubernetes couldn’t complete the check for adding a debug container. Recheck access or ask your administrator to verify it.</>
      : query.error ? <>The permission request failed before Radar received a decision for <code>{namespace}/{name}</code>. Recheck access or ask your administrator to verify it.</>
      : unverifiedImage ? <>You can open terminals in this Pod. Whether you can add a debug container is unconfirmed.</>
      : <>The cluster access check returned no decision for <code>{namespace}/{name}</code>. Recheck access or ask your administrator to verify it.</>}</p>
    {query.data?.refusedAttempt && !denied && <div role="status" className="max-w-full break-words text-warning-text">
      <p>The last debug request was refused.</p><p>{query.data.refusedAttempt.message}</p>
      {unverifiedImage && !query.data.refusedAttempt.rechecked && <p className="text-theme-text-secondary">Recheck access to review another attempt.</p>}
    </div>}
    {unverifiedImage && (!unverifiedReview?.refused || unverifiedReview.refused.rechecked) && <PodDebugAction pod={pod} defaultImage={unverifiedImage} failedContainer={target} unverifiedReview={unverifiedReview}/>}
    {denied && <p className="text-xs text-theme-text-tertiary">Pod <code>{namespace}/{name}</code></p>}
    <PodDebugAccessRequest key={denied ? 'grant' : 'verify'} namespace={namespace} name={name} target={target} denied={denied} checking={query.isFetching} onRecheck={() => void recheckAccess()} />
  </div>
}
