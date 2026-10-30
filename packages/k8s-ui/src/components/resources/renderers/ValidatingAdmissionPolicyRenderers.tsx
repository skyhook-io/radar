import type React from 'react'
import { Braces, FileWarning, Filter, Link2, ListChecks, ScrollText, ShieldCheck, Variable } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import {
  ConditionsSection,
  LabelSelectorDisplay,
  Property,
  PropertyList,
  ResourceLink,
  Section,
} from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'

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

export type BindingParameterReference =
  | { mode: 'none' }
  | { mode: 'name'; name: string; namespace?: string; parameterNotFoundAction?: string }
  | { mode: 'selector'; selector: any; namespace?: string; parameterNotFoundAction?: string }

export function getBindingParameterReference(data: any): BindingParameterReference {
  const ref = data?.spec?.paramRef
  if (!ref || typeof ref !== 'object') return { mode: 'none' }
  if (typeof ref.name === 'string' && ref.name) {
    return {
      mode: 'name',
      name: ref.name,
      ...(typeof ref.namespace === 'string' && ref.namespace ? { namespace: ref.namespace } : {}),
      ...(typeof ref.parameterNotFoundAction === 'string' ? { parameterNotFoundAction: ref.parameterNotFoundAction } : {}),
    }
  }
  if (ref.selector && typeof ref.selector === 'object') {
    return {
      mode: 'selector',
      selector: ref.selector,
      ...(typeof ref.namespace === 'string' && ref.namespace ? { namespace: ref.namespace } : {}),
      ...(typeof ref.parameterNotFoundAction === 'string' ? { parameterNotFoundAction: ref.parameterNotFoundAction } : {}),
    }
  }
  return { mode: 'none' }
}

function Expression({ value }: { value: unknown }) {
  if (typeof value !== 'string' || !value) return null
  return <pre className="font-mono text-xs text-theme-text-primary whitespace-pre-wrap break-all">{value}</pre>
}

function ExpressionCards({
  title,
  icon,
  items,
  renderDetails,
}: {
  title: string
  icon: React.ComponentType<{ className?: string }>
  items: any[]
  renderDetails?: (item: any) => React.ReactNode
}) {
  if (items.length === 0) return null
  return (
    <Section title={`${title} (${items.length})`} icon={icon} defaultExpanded>
      <div className="space-y-2">
        {items.map((item, index) => (
          <div key={`${item?.name ?? item?.key ?? index}`} className="card-inner-lg">
            {(item?.name || item?.key) && (
              <div className="mb-1 text-xs font-medium text-theme-text-secondary">{item.name ?? item.key}</div>
            )}
            <Expression value={item?.expression ?? item?.valueExpression} />
            {renderDetails?.(item)}
          </div>
        ))}
      </div>
    </Section>
  )
}

function MatchRules({ rules, label }: { rules: any[]; label: string }) {
  if (rules.length === 0) return null
  return (
    <div className="space-y-1.5">
      <div className="text-xs font-medium text-theme-text-secondary">{label}</div>
      {rules.map((rule, index) => (
        <div key={index} className="card-inner text-xs text-theme-text-secondary space-y-1">
          <div className="flex flex-wrap gap-1">
            {(rule?.operations ?? []).map((operation: string, valueIndex: number) => <Badge key={`operation:${operation}:${valueIndex}`} tone="structural" size="sm">Operation: {operation}</Badge>)}
            {(rule?.apiGroups ?? []).map((group: string, valueIndex: number) => <Badge key={`api-group:${group}:${valueIndex}`} tone="structural" size="sm">API Group: {group || 'core'}</Badge>)}
            {(rule?.apiVersions ?? []).map((version: string, valueIndex: number) => <Badge key={`api-version:${version}:${valueIndex}`} tone="structural" size="sm">API Version: {version}</Badge>)}
            {(rule?.resources ?? []).map((resource: string, valueIndex: number) => <Badge key={`resource:${resource}:${valueIndex}`} tone="structural" size="sm">Resource: {resource}</Badge>)}
          </div>
          {rule?.scope && <div>Scope: {rule.scope}</div>}
          {Array.isArray(rule?.resourceNames) && rule.resourceNames.length > 0 && (
            <div>Resource Names: {rule.resourceNames.join(', ')}</div>
          )}
        </div>
      ))}
    </div>
  )
}

function MatchConstraints({ value, title }: { value: any; title: string }) {
  const constraints = value && typeof value === 'object' ? value : {}
  const resourceRules = Array.isArray(constraints.resourceRules) ? constraints.resourceRules : []
  const excludeResourceRules = Array.isArray(constraints.excludeResourceRules) ? constraints.excludeResourceRules : []
  return (
    <Section title={title} icon={Filter} defaultExpanded>
      <div className="space-y-3">
        <PropertyList>
          <Property label="Match Policy" value={constraints.matchPolicy ?? 'Equivalent (default)'} />
        </PropertyList>
        <MatchRules rules={resourceRules} label="Included resource rules" />
        <MatchRules rules={excludeResourceRules} label="Excluded resource rules" />
        <div>
          <div className="text-xs text-theme-text-tertiary mb-1">Namespace Selector</div>
          <LabelSelectorDisplay selector={constraints.namespaceSelector} />
        </div>
        <div>
          <div className="text-xs text-theme-text-tertiary mb-1">Object Selector</div>
          <LabelSelectorDisplay selector={constraints.objectSelector} />
        </div>
      </div>
    </Section>
  )
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
            <div className="text-sm text-warning-text">{warning.warning ?? 'Unspecified type-check warning'}</div>
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

function ParameterReference({ data }: { data: any }) {
  const reference = getBindingParameterReference(data)
  if (reference.mode === 'none') {
    return <div className="text-sm text-theme-text-tertiary">No parameter reference declared.</div>
  }
  return (
    <>
      <PropertyList>
        {reference.mode === 'name' && <>
          <Property label="Name" value={reference.name} />
          <Property label="Namespace" value={reference.namespace ?? 'Not specified'} />
        </>}
        {reference.mode === 'selector' && (
          <>
            <Property label="Selector" value={<LabelSelectorDisplay selector={reference.selector} />} />
            <Property label="Namespace" value={reference.namespace ?? 'Not specified'} />
          </>
        )}
        <Property label="Parameter Not Found" value={reference.parameterNotFoundAction ?? 'Deny (default)'} />
      </PropertyList>
      <div className="mt-2 text-xs text-theme-text-tertiary">
        Parameter availability is not checked; Radar does not discover or enumerate arbitrary parameter resources.
      </div>
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
      <Section title="Parameter Reference" icon={Braces} defaultExpanded>
        <ParameterReference data={data} />
      </Section>
      <MatchConstraints value={data?.spec?.matchResources} title="Binding Match Constraints" />
      <div className="text-xs text-theme-text-tertiary">
        The effective scope is the intersection of this binding’s match constraints and the policy’s match constraints.
      </div>
    </>
  )
}
