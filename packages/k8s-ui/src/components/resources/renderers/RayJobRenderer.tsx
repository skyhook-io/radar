import type { ReactNode } from 'react'
import type { ResourceRef } from '../../../types'
import { formatRelativeAgeTime } from '../../../utils/format'
import { Badge } from '../../ui/Badge'
import { Disclosure } from '../../ui/Disclosure'
import { AlertBanner, Property, PropertyList, ResourceLink } from '../../ui/drawer-components'
import { rayJobIsLocal, rayJobSubmissionMode } from '../resource-utils-ray'

export interface RayJobRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
  submitterEvidence?: ReactNode
  selectedClusterEvidence?: ReactNode
  admissionContent?: ReactNode
}

const card = 'min-w-0 overflow-hidden rounded-lg border border-theme-border bg-theme-surface'
const header = 'border-b border-theme-border bg-theme-elevated px-3 py-2.5 text-xs font-medium uppercase tracking-wider text-theme-text-secondary'

export function RayJobRenderer({ data, onNavigate, submitterEvidence, selectedClusterEvidence, admissionContent }: RayJobRendererProps) {
  const spec = data.spec ?? {}
  const status = data.status ?? {}
  const local = rayJobIsLocal(data)
  const mode = rayJobSubmissionMode(data)
  const selectsExisting = Object.keys(spec.clusterSelector ?? {}).length > 0
  const selected = spec.clusterSelector?.['ray.io/cluster']
  const cluster = selected || status.rayClusterName
  const lifecycle = status.jobDeploymentStatus
  const jobStatus = status.jobStatus
  const failed = lifecycle === 'Failed' || lifecycle === 'ValidationFailed'
  const transitioning = ['Retrying', 'Suspending', 'Suspended'].includes(lifecycle)
  const rules: any[] = spec.deletionStrategy?.deletionRules ?? []
  const time = (value: string) => <time dateTime={value} title={value}>{formatRelativeAgeTime(value)}</time>
  return <div className="@container/rayjob space-y-4">
    {!local && <AlertBanner variant="info" title="Externally managed RayJob" message={`Managed by ${spec.managedBy}. Reported runtime names may refer to another cluster; local absence does not establish execution state.`} />}
    {admissionContent}
    <div className="grid items-start gap-4 @min-[1024px]/rayjob:grid-cols-[minmax(0,1.7fr)_minmax(320px,0.9fr)]">
      <section className={card} aria-label="RayJob execution">
        <h3 className={header}>Execution</h3>
        <div className="space-y-3 p-3 text-sm">
          <div className="flex flex-wrap gap-x-6 gap-y-3">
            <div><p className="mb-1 text-xs text-theme-text-secondary">Controller lifecycle</p><Badge severity={failed ? 'error' : transitioning ? 'warning' : 'neutral'}>{lifecycle || 'Not reported'}</Badge></div>
            <div><p className="mb-1 text-xs text-theme-text-secondary">Last reported application state</p><Badge severity={failed || transitioning ? 'neutral' : jobStatus === 'FAILED' ? 'error' : jobStatus === 'RUNNING' || jobStatus === 'SUCCEEDED' ? 'success' : 'neutral'}>{jobStatus || 'Not reported'}</Badge></div>
          </div>
          {status.reason && <p className="break-words font-medium">{status.reason}</p>}
          {status.message && <p className="max-w-3xl whitespace-pre-wrap break-words">{status.message}</p>}
          {failed && jobStatus && jobStatus !== 'FAILED' && <p className="text-xs text-theme-text-secondary">{jobStatus === 'SUCCEEDED' ? 'The application reported success, but the controller lifecycle failed. Review the controller message and submitter evidence.' : 'Controller failure does not establish that the application has stopped.'}</p>}
          {lifecycle === 'Retrying' && <p className="text-xs text-theme-text-secondary">Preparing to retry. The reported failure and submitter may belong to the attempt being cleaned up.</p>}
          <PropertyList>
            <Property label="Suspension requested" value={spec.suspend === true ? 'Yes' : 'No'} />
            {status.jobId && <Property label="Ray job ID" value={<span className="break-all font-mono">{status.jobId}</span>} />}
            {status.rayJobInfo?.startTime && <Property label="Application started" value={time(status.rayJobInfo.startTime)} />}
            {status.rayJobInfo?.endTime && <Property label="Application ended" value={time(status.rayJobInfo.endTime)} />}
            {status.failed != null && <Property label="Reported failed runs" value={status.failed} />}
            {status.succeeded != null && <Property label="Reported successful runs" value={status.succeeded} />}
          </PropertyList>
          {status.jobStatusCheckFailureStartTime && <AlertBanner variant="warning" title="Application status checks are failing" >{'Checks have been failing since '}{time(status.jobStatusCheckFailureStartTime)}. Application state is the last reported observation.</AlertBanner>}
        </div>
        <div className="border-t border-theme-border p-3 text-xs text-theme-text-secondary">
          <Disclosure summary="Timing and observation details">
            <div className="space-y-2 pt-2">
              {status.startTime && <p>Controller attempt started: {time(status.startTime)}</p>}
              {status.endTime && <p>Controller terminal time: {time(status.endTime)}</p>}
              <p>Application and controller times describe different events. Status is a controller snapshot; cleared fields are not retained as attempt history. See Timeline for recorded events and YAML for the full resource.</p>
            </div>
          </Disclosure>
        </div>
      </section>
      <section className={card} aria-label="RayJob submission and runtime">
        <h3 className={header}>Submission and runtime</h3>
        <div className="space-y-3 p-3 text-sm">
          <p className="font-medium break-words">{mode}</p>
          <p className="text-theme-text-secondary">{mode === 'K8sJobMode' ? 'A Kubernetes Job submits the application to Ray.' : mode === 'HTTPMode' ? 'The controller submits directly through the Ray API; there is no submitter Job.' : mode === 'InteractiveMode' ? 'The controller waits for spec.jobId to identify a manually submitted Ray job.' : mode === 'SidecarMode' ? 'A container in the head Pod submits the application; there is no submitter Job.' : 'Submission behavior is defined by the reported mode.'}</p>
          {cluster ? <div className="space-y-1 border-t border-theme-border pt-3">
            <p className="text-xs text-theme-text-secondary">{selected ? 'Selected existing RayCluster' : 'Reported RayCluster'}</p>
            <p className="break-all font-medium">{local ? <ResourceLink kind="rayclusters" group="ray.io" namespace={data.metadata.namespace} name={cluster} onNavigate={onNavigate} /> : cluster}</p>
            {local && <p className="text-xs text-theme-text-secondary">Open the RayCluster for head/worker Pods and their logs.</p>}
            {selectedClusterEvidence}
            {selected && status.rayClusterName && selected !== status.rayClusterName && <div className="pt-2"><p className="text-xs text-theme-text-secondary">Last reported runtime differs from the selected cluster</p>{local ? <ResourceLink kind="rayclusters" group="ray.io" namespace={data.metadata.namespace} name={status.rayClusterName} onNavigate={onNavigate} /> : <p>{status.rayClusterName}</p>}</div>}
          </div> : <p className="text-theme-text-secondary">{selectsExisting ? 'Cluster selection has no ray.io/cluster name.' : 'No runtime cluster reported.'}</p>}
          {local && mode === 'K8sJobMode' && <div className="space-y-2 border-t border-theme-border pt-3"><p className="text-xs font-medium text-theme-text-secondary">Observed submitter Job</p>{submitterEvidence ?? <p className="text-theme-text-secondary">Submitter evidence is not available.</p>}</div>}
        </div>
      </section>
    </div>
    <section className={card} aria-label="RayJob configuration">
      <h3 className={header}>Job configuration</h3>
      <div className="space-y-3 p-3 text-sm">
        <p className="text-xs text-theme-text-secondary">Entrypoint</p>
        <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words font-mono text-xs">{spec.entrypoint || 'Not specified'}</pre>
        <div className="grid gap-4 @min-[1024px]/rayjob:grid-cols-2">
          <PropertyList>
            {spec.entrypointNumCpus != null && <Property label="Entrypoint CPUs" value={spec.entrypointNumCpus} />}
            {spec.entrypointNumGpus != null && <Property label="Entrypoint GPUs" value={spec.entrypointNumGpus} />}
            {spec.entrypointResources && <Property label="Entrypoint custom resources" value={<code className="break-all">{spec.entrypointResources}</code>} />}
            {spec.backoffLimit != null && <Property label="RayJob retry limit" value={spec.backoffLimit} />}
            {spec.submitterConfig?.backoffLimit != null && <Property label="Submitter retry limit" value={spec.submitterConfig.backoffLimit} />}
            {spec.activeDeadlineSeconds != null && <Property label="Active deadline" value={`${spec.activeDeadlineSeconds}s`} />}
            {spec.preRunningDeadlineSeconds != null && <Property label="Pre-running deadline" value={`${spec.preRunningDeadlineSeconds}s`} />}
          </PropertyList>
          <div className="space-y-2 text-theme-text-secondary">
            <p className="font-medium text-theme-text-primary">Runtime cleanup</p>
            {selectsExisting ? <p>Runtime cleanup is not applied to a selected existing cluster.</p> : <>
              {!spec.deletionStrategy && <p>{spec.shutdownAfterJobFinishes === true ? `Cluster shutdown requested after completion${spec.ttlSecondsAfterFinished != null ? ` (TTL ${spec.ttlSecondsAfterFinished}s)` : ''}.` : 'No completion shutdown requested.'}</p>}
              {spec.deletionStrategy && <Disclosure summary={`Deletion policy${rules.length ? ` · ${rules.length} rules` : ''}`}>
                <div className="space-y-2 pt-2">
                  <p>Requires the operator’s RayJobDeletionPolicy feature gate. These are declared rules, not proof that cleanup occurred.</p>
                  {rules.map((rule, index) => <p className="break-words" key={index}>{rule.policy} · {rule.condition?.jobStatus ? `Application ${rule.condition.jobStatus}` : `Controller ${rule.condition?.jobDeploymentStatus}`} · TTL {rule.condition?.ttlSeconds ?? 0}s</p>)}
                  {spec.deletionStrategy.onSuccess && <p>On success: {spec.deletionStrategy.onSuccess.policy}</p>}
                  {spec.deletionStrategy.onFailure && <p>On failure: {spec.deletionStrategy.onFailure.policy}</p>}
                </div>
              </Disclosure>}
            </>}
            <p className="text-xs">Operator configuration can affect cleanup. Missing runtime resources do not establish application failure.</p>
          </div>
        </div>
      </div>
    </section>
  </div>
}
