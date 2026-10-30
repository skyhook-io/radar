import { Link2 } from 'lucide-react'
import {
  RelationshipGroup,
  ResourceLink,
  Section,
  type ResourceRef,
} from '@skyhook-io/k8s-ui'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import {
  ValidatingAdmissionPolicyBindingRenderer as BaseBindingRenderer,
  ValidatingAdmissionPolicyRenderer as BasePolicyRenderer,
} from '@skyhook-io/k8s-ui/components/resources/renderers/ValidatingAdmissionPolicyRenderers'
import { useResources, useResourceWithRelationships } from '../../../api/client'

const ADMISSION_REGISTRATION_GROUP = 'admissionregistration.k8s.io'

export function ValidatingAdmissionPolicyRenderer({
  data,
  onNavigate,
}: {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}) {
  const name = data?.metadata?.name
  const query = useResources<any>(
    'validatingadmissionpolicybindings',
    undefined,
    ADMISSION_REGISTRATION_GROUP,
    { enabled: Boolean(name), refetchInterval: 30000 },
  )
  const bindings = (query.data ?? []).filter(binding => binding?.spec?.policyName === name)
  const bindingLookupComplete = query.coverage?.status === 'complete'
  const bindingTitle = bindingLookupComplete
    ? `Bindings (${bindings.length})`
    : bindings.length > 0 ? `Bindings (${bindings.length}+)` : 'Bindings'
  const refs = bindings.map(binding => ({
    kind: 'ValidatingAdmissionPolicyBinding',
    namespace: '',
    name: binding?.metadata?.name ?? '',
    group: ADMISSION_REGISTRATION_GROUP,
  }))

  return (
    <BasePolicyRenderer
      data={data}
      onNavigate={onNavigate}
      bindings={
        <Section title={bindingTitle} icon={Link2} defaultExpanded>
          {query.isLoading ? (
            <div className="text-sm text-theme-text-tertiary">Looking for readable bindings…</div>
          ) : query.error && bindings.length === 0 ? (
            <LookupFailureNote errors={[query.error]} what="which bindings select this policy" />
          ) : bindings.length === 0 && bindingLookupComplete ? (
            <div className="text-sm text-theme-text-tertiary">
              No readable bindings select this policy; this policy has no effect.
            </div>
          ) : bindings.length === 0 ? (
            <div className="text-sm text-theme-text-tertiary">
              Binding availability could not be established; this policy may still have readable bindings.
            </div>
          ) : (
            <div className="space-y-2">
              {!bindingLookupComplete && !query.error && (
                <div className="text-sm text-theme-text-tertiary">
                  This list may be incomplete; binding coverage could not be confirmed.
                </div>
              )}
              <LookupFailureNote
                errors={[query.error]}
                what="which bindings select this policy"
                incomplete
              />
              <RelationshipGroup label="Select this policy" refs={refs} onNavigate={onNavigate} />
            </div>
          )}
        </Section>
      }
    />
  )
}

export function ValidatingAdmissionPolicyBindingRenderer({
  data,
  onNavigate,
}: {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}) {
  const policyName = data?.spec?.policyName ?? ''
  const query = useResourceWithRelationships<any>(
    'validatingadmissionpolicies',
    '',
    policyName,
    ADMISSION_REGISTRATION_GROUP,
  )
  const status = typeof query.error === 'object' && query.error !== null && 'status' in query.error
    ? (query.error as { status?: unknown }).status
    : undefined
  const reference = query.isLoading ? (
    <ReferenceState name={policyName} note="Checking policy availability…" />
  ) : status === 404 ? (
    <ReferenceState name={policyName} note="Referenced policy was not found." />
  ) : status === 403 ? (
    <ReferenceState name={policyName} note="Not permitted to check policy availability." />
  ) : query.error ? (
    <ReferenceState name={policyName} note="Could not check policy availability." />
  ) : query.data ? (
    <ResourceLink
      name={policyName}
      kind="validatingadmissionpolicies"
      namespace=""
      group={ADMISSION_REGISTRATION_GROUP}
      onNavigate={onNavigate}
    />
  ) : (
    <ReferenceState name={policyName} note="Policy availability was not checked." />
  )
  return <BaseBindingRenderer data={data} onNavigate={onNavigate} policyReference={reference} />
}

function ReferenceState({ name, note }: { name: string; note: string }) {
  return (
    <span className="space-y-0.5">
      <span className="block">{name || 'Not specified'}</span>
      <span className="block text-xs text-theme-text-tertiary">{note}</span>
    </span>
  )
}
