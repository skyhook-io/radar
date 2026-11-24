import { useLayoutEffect, useRef, useState } from 'react'
import { Bug } from 'lucide-react'
import { ActionConfirmDialog, getDefaultContainerName, getExecContainerNames, useOpenTerminal, type ActionConfirmResult } from '@skyhook-io/k8s-ui'
import { actionDialogResult } from '../../api/actions'
import { connectionHeaderValue } from '../../api/connectionBinding'
import { getApiBase } from '../../api/config'
import { createPodDebugContainer } from '../../api/podDebug'

interface PodDebugTarget {
  metadata: { uid: string; name: string; namespace: string }
  spec: {
    containers: { name: string }[]
    initContainers?: { name: string; restartPolicy?: string }[]
    os?: { name?: string }
    nodeSelector?: Record<string, string>
  }
  status: {
    containerStatuses?: ContainerStatus[]
    initContainerStatuses?: ContainerStatus[]
  }
}

interface ContainerStatus {
  name: string
  state?: Partial<Record<'running' | 'waiting' | 'terminated', object>>
}

export function PodDebugAction({ pod }: { pod: PodDebugTarget }) {
  const openTerminal = useOpenTerminal()
  const running = [...(pod.status.containerStatuses ?? []), ...(pod.status.initContainerStatuses ?? [])]
    .filter(container => container.state?.running)
  const targets = getExecContainerNames(pod).filter(name => running.some(container => container.name === name))
  const [draft, setDraft] = useState<{ target: string; image: string; base: string; connection?: string }>()
  const [result, setResult] = useState<ActionConfirmResult>()
  const [busy, setBusy] = useState(false)
  const submitting = useRef(false)
  const mounted = useRef(false)
  useLayoutEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])

  const os = pod.spec?.os?.name || pod.spec?.nodeSelector?.['kubernetes.io/os'] || pod.spec?.nodeSelector?.['beta.kubernetes.io/os']
  const { namespace, name, uid } = pod.metadata
  function show() {
    const preferred = getDefaultContainerName(pod)
    setDraft({ target: preferred && targets.includes(preferred) ? preferred : targets[0], image: '', base: getApiBase(), connection: connectionHeaderValue() })
  }

  async function start() {
    if (!draft || submitting.current || !targets.includes(draft.target)) return
    const sameConnection = () => getApiBase() === draft.base && connectionHeaderValue() === draft.connection
    if (!sameConnection()) {
      setResult({ state: 'error', message: 'The cluster connection changed. Reopen the Pod to debug it.' })
      return
    }
    submitting.current = true
    setBusy(true)
    setResult(undefined)
    try {
      const { containerName, status } = await createPodDebugContainer(namespace, name, draft.connection, {
        targetContainer: draft.target, image: draft.image.trim() || undefined, uid,
      })
      if (!mounted.current || !sameConnection()) return
      openTerminal({
        namespace, podName: name, containerName, containers: [containerName],
        debugSession: { connectionId: draft.connection },
        sessionNote: status === 'pending' ? 'The debug container was added. If it is still starting, reconnect this terminal once it is running.' : undefined,
      })
      setDraft(undefined)
    } catch (error) {
      if (mounted.current && sameConnection()) setResult(actionDialogResult(error instanceof Error ? error : new Error(String(error)), {
        unknown: 'Check the ephemeral containers in the Pod YAML before trying again',
      }))
    } finally {
      submitting.current = false
      if (mounted.current) {
        setBusy(false)
        if (!sameConnection()) setDraft(undefined)
      }
    }
  }

  if (!uid || os?.toLowerCase() === 'windows' || (!draft && !targets.length)) return null
  return <>
    <button type="button" className="btn-secondary flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium" onClick={show}>
      <Bug className="h-3.5 w-3.5" /> Debug
    </button>
    <ActionConfirmDialog open={!!draft} title="Debug Pod" subject={{ kind: 'Pod', namespace, name }}
      effect="Start an ephemeral container and open a terminal in it. Its entry stays on the Pod until the Pod is deleted."
      onClose={() => {
        if (submitting.current) return
        setDraft(undefined)
        if (result?.state === 'error') setResult(undefined)
      }}
      onConfirm={start} confirmLabel="Start debug container" isLoading={busy} result={result}
      guard={result?.state === 'unknown' && <button type="button" className="btn-secondary px-3 py-1.5 text-sm"
        onClick={() => setResult(undefined)}>I checked the Pod YAML</button>}
      incompleteReason={draft && !targets.includes(draft.target) ? 'Select a running container.' : undefined}>
      {draft && <div className="grid gap-3">
        <label className="grid gap-1">Target container
          <select className="rounded border border-theme-border bg-theme-surface px-2 py-1.5" value={draft.target} disabled={busy}
            onChange={event => setDraft({ ...draft, target: event.target.value })}>
            {!targets.includes(draft.target) && <option value={draft.target} disabled>{draft.target} (stopped)</option>}
            {targets.map(name => <option key={name} value={name}>{name}</option>)}
          </select>
        </label>
        <label className="grid gap-1">Debug image (optional)
          <input className="rounded border border-theme-border bg-theme-surface px-2 py-1.5" placeholder="Server default" value={draft.image} disabled={busy}
            onChange={event => setDraft({ ...draft, image: event.target.value })} />
        </label>
      </div>}
    </ActionConfirmDialog>
  </>
}
