import { Link2 } from 'lucide-react'
import {
  RelationshipGroup,
  ResourceLink,
  Section,
  type ResourceRef,
} from '@skyhook-io/k8s-ui'
import { isFetchError, isForbiddenError } from '@skyhook-io/k8s-ui/types/fetch-error'
import { LookupFailureNote } from '@skyhook-io/k8s-ui/components/resources/renderers/LookupFailureNote'
import {
  ValidatingAdmissionPolicyBindingRenderer as BaseBindingRenderer,
  ValidatingAdmissionPolicyRenderer as BasePolicyRenderer,
} from '@skyhook-io/k8s-ui/components/resources/renderers/ValidatingAdmissionPolicyRenderers'
import {
  MutatingAdmissionPolicyBindingRenderer as BaseMutatingBinding,
  MutatingAdmissionPolicyRenderer as BaseMutatingPolicy,
} from '@skyhook-io/k8s-ui/components/resources/renderers/MutatingAdmissionPolicyRenderers'
import { useResources, useResourceWithRelationships } from '../../../api/client'

const ADMISSION_REGISTRATION_GROUP = 'admissionregistration.k8s.io'

interface AdmissionProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

export function ValidatingAdmissionPolicyRenderer(props: AdmissionProps) {
  return <BasePolicyRenderer {...props} bindings={<PolicyBindings {...props} mutating={false} />} />
}

export function MutatingAdmissionPolicyRenderer(props: AdmissionProps) {
  return <BaseMutatingPolicy {...props} bindings={<PolicyBindings {...props} mutating />} />
}

function PolicyBindings({ data, onNavigate, mutating }: AdmissionProps & { mutating: boolean }) {
  const name = data?.metadata?.name
  const query = useResources<any>(
    mutating ? 'mutatingadmissionpolicybindings' : 'validatingadmissionpolicybindings',
    undefined,
    ADMISSION_REGISTRATION_GROUP,
    { enabled: Boolean(name), refetchInterval: 30000 },
  )
  const denied = isForbiddenError(query.error)
  const bindings = denied ? [] : (query.data ?? []).filter(binding => binding?.spec?.policyName === name)
  const bindingLookupComplete = query.coverage?.status === 'complete' && !query.error
  const bindingTitle = bindingLookupComplete
    ? `Bindings (${bindings.length})`
    : bindings.length > 0 ? `Bindings (${bindings.length}+)` : 'Bindings'
  const refs = bindings.map(binding => ({
    kind: mutating ? 'MutatingAdmissionPolicyBinding' : 'ValidatingAdmissionPolicyBinding',
    namespace: '',
    name: binding?.metadata?.name ?? '',
    group: ADMISSION_REGISTRATION_GROUP,
  }))

  return (
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
  )
}

export function ValidatingAdmissionPolicyBindingRenderer(props: AdmissionProps) {
  return <BaseBindingRenderer {...props} policyReference={<PolicyReference {...props} kind="validatingadmissionpolicies" />} />
}

export function MutatingAdmissionPolicyBindingRenderer(props: AdmissionProps) {
  return <BaseMutatingBinding {...props} policyReference={<PolicyReference {...props} kind="mutatingadmissionpolicies" />} />
}

function PolicyReference({ data, onNavigate, kind }: AdmissionProps & { kind: string }) {
  const policyName = data?.spec?.policyName ?? ''
  const query = useResourceWithRelationships<any>(
    kind,
    '',
    policyName,
    ADMISSION_REGISTRATION_GROUP,
  )
  const status = isFetchError(query.error) ? query.error.status : undefined
  if (!policyName) return <>Not specified</>
  return query.isLoading ? (
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
      kind={kind}
      namespace=""
      group={ADMISSION_REGISTRATION_GROUP}
      onNavigate={onNavigate}
    />
  ) : (
    <ReferenceState name={policyName} note="Policy availability was not checked." />
  )
}

function ReferenceState({ name, note }: { name: string; note: string }) {
  return (
    <span className="space-y-0.5">
      <span className="block">{name || 'Not specified'}</span>
      <span className="block text-xs text-theme-text-tertiary">{note}</span>
    </span>
  )
}
