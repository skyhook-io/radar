import type React from 'react'
import { Braces, FileWarning, Link2, ListChecks, ScrollText, ShieldCheck, Variable } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import {
  ConditionsSection,
  Property,
  PropertyList,
  ResourceLink,
  Section,
} from '../../ui/drawer-components'
import { BindingScope, Expression, ExpressionCards, MatchConstraints } from './AdmissionPolicySections'
import type { ResourceRef } from '../../../types'

export { getBindingParameterReference, type BindingParameterReference } from './AdmissionPolicySections'

const ADMISSION_REGISTRATION_GROUP = 'admissionregistration.k8s.io'

interface ExpressionWarning {
  fieldRef?: string
  warning?: string
}

export type TypeCheckingState =
  | { state: 'unreported'; warnings: ExpressionWarning[] }
  | { state: 'unknown'; warnings: ExpressionWarning[] }
  | { state: 'current' | 'stale'; observedGeneration: number; generation: number; warnings: ExpressionWarning[] }

export function getTypeCheckingState(data: any): TypeCheckingState {
  const generation = data?.metadata?.generation
  const observedGeneration = data?.status?.observedGeneration
  const typeChecking = data?.status?.typeChecking
  if (!typeChecking || typeof typeChecking !== 'object' || Array.isArray(typeChecking)) {
    return { state: 'unreported', warnings: [] }
  }
  const expressionWarnings = typeChecking.expressionWarnings
  if (expressionWarnings !== undefined && !Array.isArray(expressionWarnings)) {
    return { state: 'unreported', warnings: [] }
  }
  const warnings = expressionWarnings ?? []
  if ((generation !== undefined && (typeof generation !== 'number' || !Number.isFinite(generation)))
    || (observedGeneration !== undefined && (typeof observedGeneration !== 'number' || !Number.isFinite(observedGeneration)))) {
    return { state: 'unreported', warnings: [] }
  }
  if (generation === undefined || observedGeneration === undefined) {
    return { state: 'unknown', warnings }
  }
  return {
    state: observedGeneration === generation ? 'current' : 'stale',
    observedGeneration,
    generation,
    warnings,
  }
}

export function getValidationActions(data: any): string[] {
  return Array.isArray(data?.spec?.validationActions)
    ? data.spec.validationActions.filter((action: unknown): action is string => typeof action === 'string')
    : []
}

function TypeChecking({ data }: { data: any }) {
  const result = getTypeCheckingState(data)
  if (result.state === 'unreported') {
    return (
      <Section title="Type-check Warnings" icon={FileWarning} defaultExpanded>
        <div className="text-sm text-theme-text-tertiary">
          Type-checking has not been reported for this policy.
        </div>
      </Section>
    )
  }
  return (
    <Section title="Type-check Warnings" icon={FileWarning} defaultExpanded>
      <div className="space-y-2">
        {result.state === 'stale' && (
          <div className="text-sm text-warning-text">
            Stale result from observed generation {result.observedGeneration}; the policy is generation {result.generation}.
          </div>
        )}
        {result.state === 'unknown' && (
          <div className="text-sm text-theme-text-tertiary">
            Type-check warning freshness could not be determined.
          </div>
        )}
        {result.warnings.length === 0 ? (
          result.state === 'unknown'
            ? <div className="text-sm text-theme-text-tertiary">The API server reported zero type-check warnings.</div>
            : <div className="text-sm text-theme-text-tertiary">The API server reported zero type-check warnings for observed generation {result.observedGeneration}.</div>
        ) : result.warnings.map((warning, index) => (
          <div key={index} className="card-inner">
            {warning.fieldRef && <div className="font-mono text-xs text-theme-text-tertiary">{warning.fieldRef}</div>}
            <pre className="font-mono text-xs text-warning-text whitespace-pre-wrap break-words">{warning.warning ?? 'Unspecified type-check warning'}</pre>
          </div>
        ))}
      </div>
    </Section>
  )
}

export function ValidatingAdmissionPolicyRenderer({
  data,
  bindings,
}: {
  data: any
  onNavigate?: (ref: ResourceRef) => void
  bindings?: React.ReactNode
}) {
  const spec = data?.spec ?? {}
  const failurePolicy = spec.failurePolicy ?? 'Fail'
  const validations = Array.isArray(spec.validations) ? spec.validations : []
  const variables = Array.isArray(spec.variables) ? spec.variables : []
  const auditAnnotations = Array.isArray(spec.auditAnnotations) ? spec.auditAnnotations : []
  const matchConditions = Array.isArray(spec.matchConditions) ? spec.matchConditions : []
  const paramKind = spec.paramKind

  return (
    <>
      <Section title="Policy Configuration" icon={ShieldCheck} defaultExpanded>
        <PropertyList>
          <Property label="Failure Policy" value={<span className="flex items-center gap-2">
            <Badge tone="structural">{failurePolicy}</Badge>
            {spec.failurePolicy == null && <Badge tone="note" size="sm">default</Badge>}
          </span>} />
          <Property label="Parameter Kind" value={paramKind
            ? <span className="font-mono text-xs">{paramKind.apiVersion}/{paramKind.kind}</span>
            : 'No parameter kind declared'} />
        </PropertyList>
        <div className="mt-2 text-xs text-theme-text-tertiary">
          A policy declaration has no effect until at least one binding selects it.
        </div>
      </Section>

      {bindings}

      <ExpressionCards
        title="Validations"
        icon={ListChecks}
        items={validations}
        renderDetails={validation => (
          <>
            {validation?.message && <div className="mt-2 text-sm text-theme-text-secondary">{validation.message}</div>}
            {validation?.messageExpression && <div className="mt-2">
              <div className="text-xs text-theme-text-tertiary mb-1">Message Expression</div>
              <Expression value={validation.messageExpression} />
            </div>}
            {validation?.reason && <div className="mt-2"><Badge tone="structural">{validation.reason}</Badge></div>}
          </>
        )}
      />
      <MatchConstraints value={spec.matchConstraints} title="Match Constraints" />
      <ExpressionCards title="Variables" icon={Variable} items={variables} />
      <ExpressionCards title="Audit Annotations" icon={ScrollText} items={auditAnnotations} />
      <ExpressionCards title="Match Conditions" icon={Braces} items={matchConditions} />
      <TypeChecking data={data} />
      <ConditionsSection conditions={data?.status?.conditions} />
    </>
  )
}

export function ValidatingAdmissionPolicyBindingRenderer({
  data,
  onNavigate,
  policyReference,
}: {
  data: any
  onNavigate?: (ref: ResourceRef) => void
  policyReference?: React.ReactNode
}) {
  const policyName = data?.spec?.policyName
  const actions = getValidationActions(data)
  return (
    <>
      <Section title="Binding" icon={Link2} defaultExpanded>
        <PropertyList>
          <Property label="Policy" value={policyReference ?? (policyName
            ? <ResourceLink
              name={policyName}
              kind="validatingadmissionpolicies"
              namespace=""
              group={ADMISSION_REGISTRATION_GROUP}
              onNavigate={onNavigate}
            />
            : 'Not specified')} />
          <Property label="Validation Actions" value={actions.length > 0
            ? <span className="flex flex-wrap gap-1">{actions.map(action => <Badge key={action} tone="structural">{action}</Badge>)}</span>
            : 'None declared'} />
        </PropertyList>
        <div className="mt-2 text-xs text-theme-text-tertiary">
          Deny, Warn, and Audit are configured actions, not observed admission outcomes.
        </div>
      </Section>
      <BindingScope data={data} />
    </>
  )
}
