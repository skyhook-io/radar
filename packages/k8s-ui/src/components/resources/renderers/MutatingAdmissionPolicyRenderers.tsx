import type { ReactNode } from 'react'
import { Filter, Link2, ListOrdered, ShieldCheck, Variable } from 'lucide-react'
import type { ResourceRef } from '../../../types'
import { Property, PropertyList, ResourceLink, Section } from '../../ui/drawer-components'
import { BindingScope, Expression, ExpressionCards, MatchConstraints } from './AdmissionPolicySections'

interface AdmissionProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
}

export function MutatingAdmissionPolicyRenderer({ data, bindings }: AdmissionProps & { bindings?: ReactNode }) {
  const spec = data.spec ?? {}
  const mutations = spec.mutations ?? []
  return <>
    <Section title={`Mutations (${mutations.length})`} icon={ListOrdered} defaultExpanded>
      <p className="mb-2 text-xs text-theme-text-secondary">When the request matches a binding and the policy, Kubernetes evaluates these CEL patches in order.</p>
      {mutations.length === 0 ? <div className="text-sm text-theme-text-tertiary">No mutations declared.</div> :
        <ol className="space-y-2">
          {mutations.map((mutation: any, index: number) => (
            <li key={index} className="card-inner-lg">
              <div className="mb-1 text-xs font-medium text-theme-text-secondary">{index + 1}. {mutation.patchType}</div>
              <Expression value={mutation.patchType === 'ApplyConfiguration'
                ? mutation.applyConfiguration?.expression : mutation.patchType === 'JSONPatch' ? mutation.jsonPatch?.expression : undefined} />
            </li>
          ))}
        </ol>}
    </Section>
    {bindings}
    <MatchConstraints value={spec.matchConstraints} title="Policy Match Constraints" />
    <ExpressionCards title="Match Conditions" icon={Filter} items={spec.matchConditions ?? []} />
    <ExpressionCards title="Variables" icon={Variable} items={spec.variables ?? []} />
    <Section title="Policy Configuration" icon={ShieldCheck} defaultExpanded>
      <PropertyList>
        <Property label="Reinvocation" value={spec.reinvocationPolicy === 'IfNeeded'
          ? 'IfNeeded · may run again if another mutation changes the object'
          : spec.reinvocationPolicy ?? 'Not specified'} />
        <Property label="Failure Policy" value={spec.failurePolicy ?? 'Fail (default)'} />
        <Property label="Parameter Kind" value={spec.paramKind
          ? <span className="font-mono text-xs">{spec.paramKind.apiVersion}/{spec.paramKind.kind}</span>
          : 'No parameter kind declared'} />
      </PropertyList>
    </Section>
  </>
}

export function MutatingAdmissionPolicyBindingRenderer({ data, onNavigate, policyReference }: AdmissionProps & { policyReference?: ReactNode }) {
  const name = data.spec?.policyName
  return <>
    <Section title="Binding" icon={Link2} defaultExpanded>
      <Property label="Policy" value={policyReference ?? (name
        ? <ResourceLink name={name} kind="mutatingadmissionpolicies" namespace="" group="admissionregistration.k8s.io" onNavigate={onNavigate} />
        : 'Not specified')} />
    </Section>
    <BindingScope data={data} />
  </>
}
