import { Box, Settings, Cloud, ScrollText, Layers } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink, useOperationalIssuesShown} from '../../ui/drawer-components'
import { CodeViewer } from '../../ui/CodeViewer'
import {
  getCrossplaneStatus,
  getCrossplaneStatusReason,
  getProviderConfigRef,
  getExternalName,
  getManagementPolicies,
  getDeletionPolicy,
  getCrossplaneConditionTone,
  getComposingXRRef,
} from '../resource-utils-crossplane'
import { CrossplanePauseNotice } from './CrossplanePauseContext'
import { kindToPluralWithGroup } from '../../../utils/navigation'

interface ManagedResourceRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

function extractApiGroup(apiVersion?: string): string {
  if (!apiVersion) return ''
  const slash = apiVersion.indexOf('/')
  return slash < 0 ? '' : apiVersion.slice(0, slash)
}


export function ManagedResourceRenderer({ data, onNavigate }: ManagedResourceRendererProps) {
  const status = getCrossplaneStatus(data)
  const statusReason = getCrossplaneStatusReason(data)
  const providerConfigRef = getProviderConfigRef(data)
  const externalName = getExternalName(data)
  const managementPolicies = getManagementPolicies(data)
  const deletionPolicy = getDeletionPolicy(data)
  const apiGroup = extractApiGroup(data?.apiVersion)
  const namespace = data?.metadata?.namespace || ''
  const composingXR = getComposingXRRef(data)

  const forProvider = data?.spec?.forProvider
  const atProvider = data?.status?.atProvider

  const alertVariant = status.level === 'unhealthy' || status.level === 'alert' ? status.level : null
  const operationalIssuesShown = useOperationalIssuesShown()

  return (
    <>
      <CrossplanePauseNotice data={data} />

      {alertVariant && statusReason && !operationalIssuesShown && (
        <AlertBanner
          variant={alertVariant === 'unhealthy' ? 'error' : 'warning'}
          title={status.text}
          message={statusReason}
        />
      )}

      <Section title="Managed Resource" icon={Box} defaultExpanded>
        <PropertyList>
          <Property label="Kind" value={data?.kind || '-'} />
          <Property label="API Group" value={apiGroup || '-'} />
          {externalName && <Property label="External Name" value={externalName} />}
          {managementPolicies && managementPolicies.length > 0 && (
            <Property label="Management Policies" value={managementPolicies.join(', ')} />
          )}
          {deletionPolicy && <Property label="Deletion Policy" value={deletionPolicy} />}
        </PropertyList>
      </Section>

      {composingXR && (
        <Section title="Composed By" icon={Layers} defaultExpanded>
          <PropertyList>
            <Property
              label={composingXR.kind}
              value={
                <ResourceLink
                  name={composingXR.name}
                  kind={kindToPluralWithGroup(composingXR.kind, extractApiGroup(composingXR.apiVersion))}
                  namespace={namespace}
                  group={extractApiGroup(composingXR.apiVersion) || undefined}
                  onNavigate={onNavigate}
                />
              }
            />
            {composingXR.apiVersion && <Property label="API Version" value={composingXR.apiVersion} />}
          </PropertyList>
        </Section>
      )}

      {providerConfigRef && (
        <Section title="Provider" icon={Cloud} defaultExpanded>
          <PropertyList>
            <Property
              label="ProviderConfig"
              value={
                <ResourceLink
                  name={providerConfigRef.name}
                  kind="providerconfigs"
                  namespace={namespace}
                  group={extractApiGroup(providerConfigRef.apiVersion) || undefined}
                  onNavigate={onNavigate}
                />
              }
            />
            {providerConfigRef.kind && <Property label="Kind" value={providerConfigRef.kind} />}
          </PropertyList>
        </Section>
      )}

      {forProvider && (
        <Section title="Spec — forProvider" icon={Settings} defaultExpanded={false}>
          <CodeViewer code={JSON.stringify(forProvider, null, 2)} language="json" />
        </Section>
      )}

      {atProvider && (
        <Section title="Status — atProvider" icon={ScrollText} defaultExpanded={false}>
          <CodeViewer code={JSON.stringify(atProvider, null, 2)} language="json" />
        </Section>
      )}

      <ConditionsSection conditions={data?.status?.conditions} getConditionTone={condition => getCrossplaneConditionTone(data, condition)} />
    </>
  )
}
