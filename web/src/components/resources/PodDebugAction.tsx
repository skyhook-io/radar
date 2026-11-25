import { useLayoutEffect, useRef, useState } from 'react'
import { Bug } from 'lucide-react'
import { ActionConfirmDialog, getDefaultContainerName, getExecContainerNames, useOpenTerminal, type ActionConfirmResult } from '@skyhook-io/k8s-ui'
import { PodDebugAccessRequest, PodDebugDenialMessage, usePodDebugContext } from './PodDebugAccessRequest'
import { Tooltip } from '../ui/Tooltip'
import { actionDialogResult } from '../../api/actions'
import { ApiError } from '../../api/client'
import { connectionHeaderValue } from '../../api/connectionBinding'
import { getApiBase } from '../../api/config'
import { useReviewedActionMutation } from '../../api/reviewedActionMutation'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'
import { useNavCustomization } from '../../context/NavCustomization'
import { createPodDebugContainer, usePodDebugAccess } from '../../api/podDebug'

export interface PodDebugTarget {
  apiVersion: string
  kind: string
  metadata: { uid: string; name: string; namespace: string; deletionTimestamp?: string }
  spec: {
    containers: { name: string }[]
    ephemeralContainers?: { name: string; image: string; targetContainerName?: string }[]
    initContainers?: { name: string; restartPolicy?: string }[]
    os?: { name?: string }
    nodeSelector?: Record<string, string>
  }
  status: {
    phase?: string
    ephemeralContainerStatuses?: { name: string; state?: {
      running?: { startedAt?: string }
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

export function runningPodDebugTargets(pod: PodDebugTarget): string[] | undefined {
  const os = pod.spec?.os?.name || pod.spec?.nodeSelector?.['kubernetes.io/os'] || pod.spec?.nodeSelector?.['beta.kubernetes.io/os']
  if (!pod.metadata.uid || pod.apiVersion !== 'v1' || pod.kind !== 'Pod' || pod.status.phase !== 'Running' || pod.metadata.deletionTimestamp || os?.toLowerCase() === 'windows') return undefined
  const running = [...(pod.status.containerStatuses ?? []), ...(pod.status.initContainerStatuses ?? [])]
    .filter(container => container.state?.running)
  return getExecContainerNames(pod).filter(name => running.some(container => container.name === name))
}

export function canRequestDebugVerification(readOnly: boolean, error: unknown, state?: string) {
  return !readOnly && (!!error || state === 'unknown' || state === 'check_failed')
}

export function debugAccessMessage(readOnly: boolean, error: unknown, state?: string) {
  return readOnly ? 'This view can’t add a debug container.' : error ? 'The permission request failed before Radar received a decision.' :
    state === 'check_failed' ? 'Kubernetes couldn’t complete the check for adding a debug container.' :
    state === 'unavailable' ? 'Debugging is unavailable. Check Pod exec access and whether exec is enabled on this Radar.' :
    'Radar hasn’t verified access to add a debug container to this Pod.'
}

export function usePodDetailAccess(pod: PodDebugTarget | undefined, namespace: {
  canExec: boolean; canOpenEphemeralTerminal: boolean; podDebugImage?: string
}) {
  const { embedded } = useNavCustomization()
  const { deployment } = useCapabilitiesContext()
  const eligible = pod?.apiVersion === 'v1' && pod.kind === 'Pod' && pod.status.phase === 'Running' && !pod.metadata.deletionTimestamp
  const execOnly = !!embedded || deployment?.mode === 'cloud' || !pod || !runningPodDebugTargets(pod)?.length
  const { query, unverifiedImage, unverifiedReview, recheckAccess } = usePodDebugAccess(pod?.metadata.namespace ?? '', pod?.metadata.name ?? '', pod?.metadata.uid ?? '',
    !!eligible && (!namespace.canExec || (!execOnly && !namespace.podDebugImage)), execOnly, eligible && !execOnly ? namespace.podDebugImage : undefined)
  const named = eligible && !query.error ? query.data : undefined
  const debugImage = execOnly || query.error || named?.state === 'denied' || named?.state === 'unavailable' || named?.state === 'check_failed' ? undefined :
    named?.state === 'allowed' ? named.image : namespace.podDebugImage || unverifiedImage
  const readOnly = !!embedded || deployment?.mode === 'cloud'
  return {
    canExec: namespace.canExec || named?.exec === true,
    canOpenEphemeralTerminal: namespace.canOpenEphemeralTerminal || named?.exec === true,
    debugImage,
    reviewStatus: { message: debugAccessMessage(readOnly, query.error, named?.state), canVerify: canRequestDebugVerification(readOnly, query.error, named?.state), refusal: readOnly ? undefined : query.data?.refusedAttempt?.message,
      recheck: readOnly ? undefined : recheckAccess, checking: query.isFetching },
    debugDenial: !execOnly && named?.state === 'denied' ? { refusal: named.refusedAttempt?.message } : undefined,
    unverifiedReview: namespace.podDebugImage ? undefined : unverifiedReview,
  }
}

export function PodDebugAction({ pod, defaultImage, failedContainer, unverifiedReview, debugDenial, reviewStatus, hideLauncher }: {
  pod: PodDebugTarget; defaultImage?: string; hideLauncher?: boolean; failedContainer?: string; debugDenial?: { refusal?: string }; unverifiedReview?: ReturnType<typeof usePodDebugAccess>['unverifiedReview']; reviewStatus?: ReturnType<typeof usePodDetailAccess>['reviewStatus']
}) {
  const openTerminal = useOpenTerminal()
  const eligibleTargets = runningPodDebugTargets(pod)
  const targets = eligibleTargets ?? []
  const context = usePodDebugContext()
  const [draft, setDraft] = useState<{ target: string; image: string; base: string; connection?: string; context?: string }>()
  const [result, setResult] = useState<ActionConfirmResult>()
  const submitting = useRef(false)
  const mounted = useRef(false)
  useLayoutEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])

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
    }, '',
  )
  const changedConnection = !!draft && (draft.base !== getApiBase() || draft.connection !== connection)
  const blockedMessage = changedConnection ? 'The cluster connection changed. Reopen the Pod to debug it.' :
    !eligibleTargets?.length ? 'This Pod no longer has a running container eligible for debugging.' :
    !debugDenial && !defaultImage ? reviewStatus?.message : undefined
  const busy = mutation.isPending
  const refused = unverifiedReview?.refused
  const refusedNow = result?.state === 'error' && mutation.error instanceof ApiError && mutation.error.status === 403 && !!unverifiedReview
  const recheckRequired = !!refused && !refused.rechecked
  const outcome = result ?? actionDialogResult(mutation.outcomeError, {
    unknown: 'Radar could not confirm whether the debug container was added',
  })
  function show() {
    if (!defaultImage && !debugDenial) return
    const preferred = failedContainer || getDefaultContainerName(pod)
    const connection = connectionHeaderValue()
    setDraft({ target: preferred && targets.includes(preferred) ? preferred : targets[0], image: defaultImage ?? '', base: getApiBase(), connection, context })
  }

  async function start() {
    if (!draft || !defaultImage || debugDenial || blockedMessage || submitting.current || refusedNow || recheckRequired || !targets.includes(draft.target)) return
    const sameConnection = () => getApiBase() === draft.base && connectionHeaderValue() === draft.connection
    submitting.current = true
    setResult(undefined)
    try {
      const { containerName, image } = await mutation.mutateAsync({ request: {
        base: draft.base, reviewedContext: draft.connection ?? '', targetContainer: draft.target, image: draft.image.trim() === defaultImage ? undefined : draft.image.trim() || undefined,
      } })
      if (!sameConnection()) return
      openTerminal({
        namespace, podName: name, containerName, containers: [containerName],
        debugSession: { connectionId: draft.connection, apiBase: draft.base, podUID: uid, image },
      })
      if (mounted.current) setDraft(undefined)
    } catch (error) {
      if (mounted.current && sameConnection()) setResult(actionDialogResult(error instanceof Error ? error : new Error(String(error)), {
        unknown: 'Radar could not confirm whether the debug container was added',
      }))
    } finally {
      submitting.current = false
      if (mounted.current && !sameConnection()) setDraft(undefined)
    }
  }

  if (!draft && (!targets.length || (!defaultImage && !debugDenial))) return null
  return <>
    {!hideLauncher && targets.length > 0 && (debugDenial ? <Tooltip content="View the permission and copy a request for your administrator">
      <button type="button" className="btn-secondary flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium" onClick={show}><Bug className="h-3.5 w-3.5" />Debug</button>
    </Tooltip> : defaultImage && <button type="button" className={`${failedContainer ? 'btn-brand' : 'btn-secondary'} flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium`} onClick={show}>
      <Bug className="h-3.5 w-3.5" />{unverifiedReview ? recheckRequired ? 'Review debug refusal' : refused ? 'Review another debug attempt' : 'Review debug container' : failedContainer ? 'Start a debug container' : 'Debug'}
    </button>)}
    <ActionConfirmDialog open={!!draft} title="Debug Pod" context={draft?.context} writes={debugDenial || blockedMessage ? [] : [{ summary: `Update pods/ephemeralcontainers on ${namespace}/${name}` }]} subject={{ kind: 'Pod', namespace, name }}
      effect={blockedMessage || (debugDenial ? <PodDebugDenialMessage /> : 'Start an ephemeral container and open a terminal in it. Its entry stays on the Pod until the Pod is deleted.')}
      onClose={() => {
        if (submitting.current) return
        setDraft(undefined)
        if (refusedNow && mutation.error) unverifiedReview?.rememberRefusal(mutation.error.message)
        if (result?.state === 'error') setResult(undefined)
      }}
      onConfirm={start} confirmLabel="Start debug container" isLoading={busy} result={outcome}
      guard={outcome?.state === 'unknown' && <div className="space-y-2">
        <p>Inspect the Pod YAML for an added ephemeral container before you retry.</p>
        <button type="button" className="btn-secondary px-3 py-1.5 text-sm"
        onClick={() => {
          mutation.acknowledgeOutcome()
          setResult(undefined)
        }}>I checked the Pod YAML</button></div>}
      incompleteReason={blockedMessage ? 'Creation is unavailable.' : debugDenial ? 'Ask your cluster administrator for access.' : refusedNow ? 'Close to recheck access after the refusal.' : recheckRequired ? 'Recheck access before reviewing another attempt.' : draft && !targets.includes(draft.target) ? 'Select a running container.' : undefined}>
      {blockedMessage && !changedConnection && <>
        {reviewStatus?.refusal && <div className="text-warning-text"><p>The last debug request was refused.</p><p className="break-words">{reviewStatus.refusal}</p></div>}
        {reviewStatus?.canVerify && draft ? <PodDebugAccessRequest namespace={namespace} name={name} target={draft.target} context={draft.context}
          denied={false} checking={reviewStatus.checking} onRecheck={reviewStatus.recheck} /> :
          reviewStatus?.recheck && <button type="button" className="btn-secondary px-2 py-1" disabled={reviewStatus.checking} onClick={() => void reviewStatus.recheck?.()}>Recheck access</button>}
      </>}
      {!blockedMessage && debugDenial && draft && <>
        {debugDenial.refusal && <div className="text-warning-text"><p>The last debug request was refused.</p><p className="break-words">{debugDenial.refusal}</p></div>}
        <PodDebugAccessRequest namespace={namespace} name={name} target={draft.target} context={draft.context} denied checking={reviewStatus?.checking} onRecheck={reviewStatus?.recheck} />
      </>}
      {draft && !blockedMessage && !debugDenial && <div className="grid gap-3">
        {refused && <div className="space-y-1 text-sm text-warning-text"><p>The last debug request was refused.</p><p className="break-words">{refused.message}</p>
          {recheckRequired && <button type="button" className="btn-secondary px-2 py-1" onClick={() => void unverifiedReview?.recheckAccess()}>Recheck access</button>}</div>}
        {unverifiedReview && <>
          <p className="text-sm text-theme-text-secondary">Radar verified terminal access to this Pod. Kubernetes will decide whether you can add a debug container when you confirm.</p>
          <PodDebugAccessRequest namespace={namespace} name={name} target={draft.target} context={draft.context} denied={false} />
        </>}
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
