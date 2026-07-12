import { Clock, Pause } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink, ConditionsSection } from '../../ui/drawer-components'
import { formatAge, cronToHuman } from '../resource-utils'

interface CronWorkflowRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function CronWorkflowRenderer({ data, onNavigate }: CronWorkflowRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const workflowSpec = spec.workflowSpec || {}
  const schedules = Array.isArray(spec.schedules) ? spec.schedules : spec.schedule ? [spec.schedule] : []
  const isSuspended = spec.suspend === true
  const active = Array.isArray(status.active) ? status.active : []
  const workflowTemplateRef = workflowSpec.workflowTemplateRef || null
  const templateName = workflowTemplateRef?.name || workflowTemplateRef?.template || ''
  const templateClusterScope = workflowTemplateRef?.clusterScope === true
  const hasNeverRun = !status.lastScheduledTime && active.length === 0

  return (
    <>
      {isSuspended && (
        <AlertBanner
          variant="info"
          icon={Pause}
          title="CronWorkflow Suspended"
          message="No new Workflows will be scheduled until this CronWorkflow is resumed."
        />
      )}

      {hasNeverRun && !isSuspended && (
        <AlertBanner
          variant="info"
          title="Never Scheduled"
          message="This CronWorkflow has no retained active or historical Workflow runs in status."
        />
      )}

      <Section title="Schedule" icon={Clock}>
        <PropertyList>
          <Property label={schedules.length === 1 ? 'Schedule' : 'Schedules'} value={schedules.join(', ')} />
          {schedules.length === 1 && <Property label="Human" value={cronToHuman(schedules[0])} />}
          <Property label="Timezone" value={spec.timezone || 'Cluster default'} />
          <Property label="Suspend" value={isSuspended ? 'Yes' : 'No'} />
          <Property label="Concurrency" value={spec.concurrencyPolicy || 'Allow'} />
          <Property label="Last Scheduled" value={status.lastScheduledTime ? formatAge(status.lastScheduledTime) : 'Never'} />
          <Property label="Active Workflows" value={active.length} />
        </PropertyList>
      </Section>

      <Section title="Workflow Template">
        <PropertyList>
          {templateName && (
            <Property
              label="Template Ref"
              value={
                <ResourceLink
                  name={templateName}
                  kind={templateClusterScope ? 'clusterworkflowtemplates' : 'workflowtemplates'}
                  namespace={templateClusterScope ? '' : data.metadata?.namespace || ''}
                  group="argoproj.io"
                  label={templateClusterScope ? `${templateName} (cluster)` : templateName}
                  onNavigate={onNavigate}
                />
              }
            />
          )}
          <Property label="Entrypoint" value={workflowSpec.entrypoint || '-'} />
          <Property label="Arguments" value={(workflowSpec.arguments?.parameters || []).length} />
          <Property label="Templates" value={(workflowSpec.templates || []).length} />
        </PropertyList>
      </Section>

      <Section title="History">
        <PropertyList>
          <Property label="Success History" value={spec.successfulJobsHistoryLimit ?? 3} />
          <Property label="Failed History" value={spec.failedJobsHistoryLimit ?? 1} />
          <Property label="Starting Deadline" value={spec.startingDeadlineSeconds ? `${spec.startingDeadlineSeconds}s` : 'None'} />
        </PropertyList>
      </Section>

      {active.length > 0 && (
        <Section title="Active Workflows">
          <div className="space-y-1">
            {active.map((wf: any) => (
              <div key={`${wf.namespace || data.metadata?.namespace || ''}/${wf.name}`} className="text-sm">
                <ResourceLink name={wf.name} kind="workflows" namespace={wf.namespace || data.metadata?.namespace || ''} group="argoproj.io" onNavigate={onNavigate} />
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
