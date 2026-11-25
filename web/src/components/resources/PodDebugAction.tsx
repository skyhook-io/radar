import { useLayoutEffect, useRef, useState } from 'react'
import { Bug } from 'lucide-react'
import { ActionConfirmDialog, getDefaultContainerName, getExecContainerNames, useOpenTerminal, type ActionConfirmResult } from '@skyhook-io/k8s-ui'
import { actionDialogResult } from '../../api/actions'
import { ApiError } from '../../api/client'
import { connectionHeaderValue } from '../../api/connectionBinding'
import { getApiBase } from '../../api/config'
import { useReviewedActionMutation } from '../../api/reviewedActionMutation'
import { createPodDebugContainer } from '../../api/podDebug'

export interface PodDebugTarget {
  apiVersion: string
  kind: string
  metadata: { uid: string; name: string; namespace: string; deletionTimestamp?: string }
  spec: {
    containers: { name: string }[]
    initContainers?: { name: string; restartPolicy?: string }[]
    os?: { name?: string }
    nodeSelector?: Record<string, string>
  }
  status: {
    phase?: string
    ephemeralContainerStatuses?: { name: string; state?: {
      running?: object
      waiting?: { reason?: string; message?: string }
      terminated?: { exitCode: number; reason?: string; message?: string }
    } }[]
    containerStatuses?: ContainerStatus[]
    initContainerStatuses?: ContainerStatus[]
  }
}

interface ContainerStatus {
  name: string
  state?: Partial<Record<'running' | 'waiting' | 'terminated', object>>
}

export function PodDebugAction({ pod, defaultImage, failedContainer }: { pod: PodDebugTarget; defaultImage: string; failedContainer?: string }) {
  const openTerminal = useOpenTerminal()
  const running = [...(pod.status.containerStatuses ?? []), ...(pod.status.initContainerStatuses ?? [])]
    .filter(container => container.state?.running)
  const targets = getExecContainerNames(pod).filter(name => running.some(container => container.name === name))
  const [draft, setDraft] = useState<{ target: string; image: string; base: string; connection?: string }>()
  const [result, setResult] = useState<ActionConfirmResult>()
  const submitting = useRef(false)
  const mounted = useRef(false)
  useLayoutEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])

  const os = pod.spec?.os?.name || pod.spec?.nodeSelector?.['kubernetes.io/os'] || pod.spec?.nodeSelector?.['beta.kubernetes.io/os']
  const { namespace, name, uid } = pod.metadata
  const connection = connectionHeaderValue()
  const mutation = useReviewedActionMutation(
    JSON.stringify([getApiBase(), 'pod-debug', uid]), connection ?? '', {
      mutationFn: ({ request }: { request: { reviewedContext: string; base: string; targetContainer: string; image?: string } }) => {
        if (getApiBase() !== request.base || (connectionHeaderValue() ?? '') !== request.reviewedContext) {
          throw new ApiError('The cluster connection changed. Reopen the Pod to debug it.', 409)
        }
        return createPodDebugContainer(namespace, name, request.reviewedContext || undefined, {
          uid, targetContainer: request.targetContainer, image: request.image,
        })
      },
    }, 'Check the ephemeral containers in the Pod YAML before trying again.',
  )
  const busy = mutation.isPending
  const outcome = result ?? actionDialogResult(mutation.outcomeError, {
    unknown: 'Radar could not confirm whether the debug container was added',
  })
  function show() {
    const preferred = failedContainer || getDefaultContainerName(pod)
    setDraft({ target: preferred && targets.includes(preferred) ? preferred : targets[0], image: defaultImage, base: getApiBase(), connection: connectionHeaderValue() })
  }

  async function start() {
    if (!draft || submitting.current || !targets.includes(draft.target)) return
    const sameConnection = () => getApiBase() === draft.base && connectionHeaderValue() === draft.connection
    submitting.current = true
    setResult(undefined)
    try {
      const { containerName, image } = await mutation.mutateAsync({ request: {
        base: draft.base, reviewedContext: draft.connection ?? '', targetContainer: draft.target, image: draft.image.trim() === defaultImage ? undefined : draft.image.trim() || undefined,
      } })
      if (!mounted.current || !sameConnection()) return
      openTerminal({
        namespace, podName: name, containerName, containers: [containerName],
        debugSession: { connectionId: draft.connection, apiBase: draft.base, podUID: uid, image },
      })
      setDraft(undefined)
    } catch (error) {
      if (mounted.current && sameConnection()) setResult(actionDialogResult(error instanceof Error ? error : new Error(String(error)), {
        unknown: 'Radar could not confirm whether the debug container was added',
      }))
    } finally {
      submitting.current = false
      if (mounted.current && !sameConnection()) setDraft(undefined)
    }
  }

  if (!uid || pod.apiVersion !== 'v1' || pod.kind !== 'Pod' || pod.status.phase !== 'Running' || pod.metadata.deletionTimestamp || os?.toLowerCase() === 'windows' || (!draft && !targets.length)) return null
  return <>
    <button type="button" className="btn-secondary flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium" onClick={show}>
      <Bug className="h-3.5 w-3.5" />{failedContainer ? 'Debug with an ephemeral container' : 'Debug'}
    </button>
    <ActionConfirmDialog open={!!draft} title="Debug Pod" subject={{ kind: 'Pod', namespace, name }}
      effect="Start an ephemeral container and open a terminal in it. Its entry stays on the Pod until the Pod is deleted."
      onClose={() => {
        if (submitting.current) return
        setDraft(undefined)
        if (result?.state === 'error') setResult(undefined)
      }}
      onConfirm={start} confirmLabel="Start debug container" isLoading={busy} result={outcome}
      guard={outcome?.state === 'unknown' && <button type="button" className="btn-secondary px-3 py-1.5 text-sm"
        onClick={() => {
          mutation.acknowledgeOutcome()
          setResult(undefined)
        }}>I checked the Pod YAML</button>}
      incompleteReason={draft && !targets.includes(draft.target) ? 'Select a running container.' : undefined}>
      {draft && <div className="grid gap-3">
        <label className="grid gap-1">Target container
          <select className="rounded border border-theme-border bg-theme-surface px-2 py-1.5" value={draft.target} disabled={busy}
            onChange={event => setDraft({ ...draft, target: event.target.value })}>
            {!targets.includes(draft.target) && <option value={draft.target} disabled>{draft.target} (stopped)</option>}
            {targets.map(name => <option key={name} value={name}>{name}</option>)}
          </select>
        </label>
        <label className="grid gap-1">Debug image
          <input className="rounded border border-theme-border bg-theme-surface px-2 py-1.5" placeholder={defaultImage} value={draft.image} disabled={busy}
            onChange={event => setDraft({ ...draft, image: event.target.value })} />
          <span className="text-xs text-theme-text-tertiary">
            {draft.image.trim() && draft.image.trim() !== defaultImage
              ? <>Server default: <code>{defaultImage}</code></>
              : 'Using the server default. Edit to override it for this container.'}
          </span>
        </label>
      </div>}
    </ActionConfirmDialog>
  </>
}
