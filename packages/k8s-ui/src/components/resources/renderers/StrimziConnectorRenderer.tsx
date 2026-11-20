import { useState } from 'react'
import type { ResourceRef } from '../../../types'
import { Badge } from '../../ui/Badge'
import { healthToSeverity } from '../../../utils/badge-colors'
import { Section, Property, PropertyList, ResourceLink, ConditionsSection, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import { getStrimziConnectorStatus } from '../resource-utils-strimzi'

export function StrimziConnectorRenderer({ data, onNavigate }: { data: any; onNavigate?: (ref: ResourceRef) => void }) {
  const [showAll, setShowAll] = useState(false)
  const state = getStrimziConnectorStatus(data)
  const operationalIssuesShown = useOperationalIssuesShown()
  const spec = data.spec ?? {}
  const status = data.status ?? {}
  const runtime = status.connectorStatus ?? {}
  const conditions = Array.isArray(status.conditions) ? status.conditions
    .filter((condition: any) => typeof condition?.type === 'string' && typeof condition?.status === 'string')
    .map((condition: any) => ({ type: condition.type, status: condition.status, message: typeof condition.message === 'string' && condition.message.trim() ? 'Strimzi condition message withheld; inspect status.conditions in YAML.' : undefined, reason: typeof condition.reason === 'string' ? condition.reason : undefined, lastTransitionTime: typeof condition.lastTransitionTime === 'string' ? condition.lastTransitionTime : undefined })) : []
  const tasks: any[] = Array.isArray(runtime.tasks) ? [...runtime.tasks].sort((a, b) => (a?.state === 'FAILED' ? 0 : a?.state === 'RUNNING' ? 2 : 1) - (b?.state === 'FAILED' ? 0 : b?.state === 'RUNNING' ? 2 : 1)) : []
  const cluster = data.metadata?.labels?.['strimzi.io/cluster']
  const historical = !state.snapshotCurrent
  return <>
    <Section title="Connector" defaultExpanded>
      <PropertyList>
        <Property label="Operator snapshot" value={<Badge severity={healthToSeverity(state.level)}>{state.text}</Badge>} />
        <Property label="KafkaConnect" value={cluster ? <ResourceLink kind="KafkaConnect" group="kafka.strimzi.io" name={cluster} namespace={data.metadata?.namespace} onNavigate={onNavigate} /> : 'Not specified'} />
        <Property label="Connector class" value={<span className="break-all">{spec.class ?? 'Not specified'}</span>} />
        <Property label="Desired state" value={spec.state ?? (data.apiVersion === 'kafka.strimzi.io/v1beta2' && spec.pause === true ? 'paused (spec.pause)' : 'running (default)')} />
        <Property label="Maximum tasks" value={spec.tasksMax ?? 'Not specified'} />
      </PropertyList>
      {!operationalIssuesShown && state.level === 'degraded' && <AlertBanner variant="warning" title={state.text} message="Strimzi reports a failure in this operator snapshot. Inspect task states, condition details in YAML, and worker logs." />}
    </Section>
    <Section title="Reported Runtime" defaultExpanded>
      <p className="mb-3 text-xs text-theme-text-secondary">{historical ? 'Historical or unassessed status. These observations do not establish current runtime health.' : 'Strimzi operator snapshot, not a live runtime check. Runtime state may have changed since reconciliation.'}</p>
      <PropertyList>
        <Property label="Connector state" value={typeof runtime.connector?.state === 'string' ? runtime.connector.state : 'Not reported'} />
        <Property label="Worker" value={<span className="break-all">{typeof runtime.connector?.worker_id === 'string' ? runtime.connector.worker_id : 'Not reported'}</span>} />
        <Property label="Observed / desired generation" value={`${status.observedGeneration ?? 'Unknown'} / ${data.metadata?.generation ?? 'Unknown'}`} />
      </PropertyList>
      <h4 className="mt-4 mb-2 text-sm font-medium">Reported tasks ({tasks.length})</h4>
      {tasks.length === 0 ? <p className="text-sm text-theme-text-secondary">No task states reported. This does not establish that tasks are running.</p> : <>
        <div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr className="text-theme-text-secondary"><th className="pr-3 pb-2">Task</th><th className="pr-3 pb-2">State</th><th className="pb-2">Worker</th></tr></thead><tbody>
          {(showAll ? tasks.slice(0, 100) : tasks.slice(0, 5)).map((task, index) => <tr key={index} className="border-t border-theme-border"><td className="py-2 pr-3">{Number.isInteger(task?.id) && task.id >= 0 ? task.id : 'Unknown'}</td><td className="py-2 pr-3">{typeof task?.state === 'string' ? task.state : 'Not reported'}</td><td className="py-2 break-all">{typeof task?.worker_id === 'string' ? task.worker_id : 'Not reported'}</td></tr>)}
        </tbody></table></div>
        {tasks.length > 5 && <button type="button" className="mt-2 text-sm text-accent-text hover:underline" onClick={() => setShowAll(!showAll)}>{showAll ? 'Show fewer tasks' : `Show ${Math.min(tasks.length, 100)} tasks`}</button>}
        {tasks.length > 100 && <p className="mt-2 text-xs text-theme-text-secondary">Task preview is limited to 100 of {tasks.length}. Inspect YAML for the full reported list.</p>}
      </>}
      <p className="mt-3 text-xs text-theme-text-tertiary">For failure details, inspect task traces in YAML and worker logs. Open KafkaConnect and follow its related resources to the worker Pods. A task count below the configured maximum is not itself a failure.</p>
    </Section>
    {(spec.autoRestart || status.autoRestart) && <Section title="Automatic Restart" defaultExpanded>
      <PropertyList>
        <Property label="Enabled" value={spec.autoRestart?.enabled === undefined ? spec.autoRestart ? 'Yes (default)' : 'Not specified' : spec.autoRestart.enabled ? 'Yes' : 'No'} />
        {spec.autoRestart?.enabled !== false && <Property label="Maximum restarts" value={spec.autoRestart?.maxRestarts ?? (spec.autoRestart ? 'Unlimited (default)' : 'Not specified')} />}
        <Property label="Reported restarts" value={status.autoRestart?.count ?? 'Not reported'} />
        <Property label="Last reported restart" value={status.autoRestart?.lastRestartTimestamp ?? 'Not reported'} />
      </PropertyList>
    </Section>}
    {/* Topic inventory is secondary to identifying the failing task. */}
    {Array.isArray(status.topics) && status.topics.length > 0 && <Section title={`Reported Topics (${status.topics.length})`} defaultExpanded={false}><p className="break-all text-sm">{status.topics.slice(0, 20).join(', ')}{status.topics.length > 20 && `; ${status.topics.length - 20} more in YAML`}</p></Section>}
    <ConditionsSection conditions={conditions} getConditionTone={condition => historical ? 'unknown' : condition.type === 'NotReady' && condition.status === 'True' ? 'fail' : condition.type === 'Warning' && condition.status === 'True' ? 'warning' : 'neutral'} />
  </>
}
