import { type ReactNode, useState } from 'react'
import { Disclosure } from '@skyhook-io/k8s-ui/components/ui/Disclosure'
import { Terminal } from 'lucide-react'
import { formatAge, getRunningEphemeralContainers, useOpenTerminal } from '@skyhook-io/k8s-ui'
import { PodDebugPermission } from '../resources/PodDebugPermission'
import { useResource } from '../../api/client'
import { connectionHeaderValue, useCurrentConnectionId } from '../../api/connectionBinding'
import { getApiBase } from '../../api/config'
import { usePodDebugAccess } from '../../api/podDebug'
import { useNavCustomization } from '../../context/NavCustomization'
import { useCapabilitiesContext, useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { PodDebugAction, debugAccessMessage, runningPodDebugTargets, type PodDebugTarget } from '../resources/PodDebugAction'

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
    {(alternatives.length > 0 || untargeted.length > 0) && <Disclosure summary="Other containers"
      className="max-w-full text-xs text-theme-text-secondary" summaryClassName="justify-center hover:text-theme-text-primary">
      <div className="mt-2 space-y-2 [overflow-wrap:anywhere]">
        {alternatives.length > 0 && <p>Try a terminal in {alternatives.map((name, index) => <span key={name}>{index > 0 && ', '}{props.switchContainer
          ? <button type="button" className="font-mono underline underline-offset-2" onClick={() => props.switchContainer?.(name)}>{name}</button>
          : <code>{name}</code>}</span>)}.</p>}
        {untargeted.length > 0 && <p>Pod-wide {untargeted.length === 1 ? 'debugger' : 'debuggers'}: <code>{untargeted.map(c => c.name).join(', ')}</code>. {podDetailsLink}</p>}
      </div>
    </Disclosure>}
  </div>
}

function DebugOptions({ namespace, name, uid, target, connection, base, pod }: RecoveryTarget & {pod: PodDebugTarget}) {
  const { embedded } = useNavCustomization()
  const { deployment } = useCapabilitiesContext()
  const { podDebugImage, canOpenEphemeralTerminal } = useNamespacedCapabilities(namespace)
  const execOnly = !!embedded || deployment?.mode === 'cloud'
  const current = () => getApiBase() === base && connectionHeaderValue() === connection
  const running = getRunningEphemeralContainers(pod).filter(c => c.targetContainerName === target)
  const { query, support, recheckSupport, unverifiedImage, unverifiedReview, recheckAccess } = usePodDebugAccess(namespace, name, uid,
    execOnly ? !!running.length && !canOpenEphemeralTerminal : !running.length && !podDebugImage,
    execOnly, !execOnly && !running.length ? podDebugImage : undefined)
  const openTerminal = useOpenTerminal()
  const [choice, setChoice] = useState('')
  const access = query.error ? undefined : query.data
  const allowedImage = query.error ? undefined : access?.state === 'allowed' ? access.image :
    access?.state === 'denied' || access?.state === 'unavailable' || access?.state === 'check_failed' ? undefined : podDebugImage
  const denied = access?.state === 'denied'
  const canCreate = !execOnly && !running.length
  const image = canCreate ? allowedImage || unverifiedImage : undefined
  const showRecheck = canCreate && (!unverifiedImage || !!unverifiedReview?.refused) && !allowedImage && support === 'supported' && (!query.isPending || !!query.error) && access?.state !== 'unavailable'
  return <div className="flex w-full min-w-0 max-w-md flex-col items-center gap-2 text-xs text-theme-text-secondary">
    {renderOptions()}
    <PodDebugAction pod={pod} defaultImage={image} failedContainer={target} unverifiedReview={image ? unverifiedReview : undefined}
      debugDenial={canCreate && denied ? { refusal: access.refusedAttempt?.message } : undefined}
      hideLauncher={!image || (!!unverifiedReview?.refused && !unverifiedReview.refused.rechecked)}
      reviewStatus={{ message: debugAccessMessage(execOnly, query.error, access?.state), refusal: query.data?.refusedAttempt?.message,
        recheck: execOnly ? undefined : recheckAccess, checking: query.isFetching }} />
    {showRecheck && <button className="btn-secondary rounded px-2 py-1" disabled={query.isFetching} onClick={() => void recheckAccess()}>Recheck access</button>}
  </div>

  function renderOptions() {
    if (running.length && support === 'supported' && (!execOnly || canOpenEphemeralTerminal || (!query.error && query.data?.exec))) {
      const selected = running.find(c => c.name === choice) ?? running[0]
      return <div className="flex w-full min-w-0 max-w-md flex-col items-center gap-2">
        <p className="text-xs text-theme-text-secondary [overflow-wrap:anywhere]">{running.length === 1 ? <>A debug container (<code>{selected.image}</code>) is running</> : `${running.length} debug containers are running`} for <code>{target}</code>.</p>
        {running.length > 1 && <select aria-label="Running debug container" className="w-full min-w-0 max-w-full rounded border border-theme-border bg-theme-surface px-2 py-1 text-xs"
          value={selected.name} onChange={event => setChoice(event.target.value)}>
          {running.map(c => <option key={c.name} value={c.name}>{c.startedAt && `${formatAge(c.startedAt)} ago · `}{c.name} · {c.image}</option>)}
        </select>}
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
    if (allowedImage) return null
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
    return <div className="flex max-w-md flex-col items-center gap-2 text-xs text-theme-text-secondary">
      <p>{denied ? <PodDebugPermission namespace={namespace} name={name} />
        : access?.state === 'check_failed' ? <>You can open terminals in this Pod. Kubernetes couldn’t complete the check for adding a debug container.</>
        : query.error ? <>The permission request failed before Radar received a decision for <code>{namespace}/{name}</code>.</>
        : unverifiedImage ? <>You can open terminals in this Pod. Whether you can add a debug container is unconfirmed.</>
        : <>The cluster access check returned no decision for <code>{namespace}/{name}</code>.</>}</p>
      {query.data?.refusedAttempt && !denied && <div role="status" className="max-w-full break-words text-warning-text">
        <p>The last debug request was refused.</p><p>{query.data.refusedAttempt.message}</p>
        {unverifiedImage && !query.data.refusedAttempt.rechecked && <p className="text-theme-text-secondary">Recheck access to review another attempt.</p>}
      </div>}
    </div>
  }
}
