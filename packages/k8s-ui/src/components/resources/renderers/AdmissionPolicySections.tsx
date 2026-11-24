import type React from 'react'
import { Braces, Filter } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import { LabelSelectorDisplay, Property, PropertyList, Section } from '../../ui/drawer-components'

const PARAM_NAMESPACE_UNSET = 'Not set: a namespaced parameter is read from the evaluated object’s namespace'

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

export function Expression({ value }: { value: unknown }) {
  if (typeof value !== 'string' || !value) return null
  return <pre className="font-mono text-xs text-theme-text-primary whitespace-pre-wrap break-all">{value}</pre>
}

export function ExpressionCards({
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

export function MatchConstraints({ value, title }: { value: any; title: string }) {
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

export function BindingScope({ data }: { data: any }) {
  return <>
    <Section title="Parameter Reference" icon={Braces} defaultExpanded>
      <ParameterReference data={data} />
    </Section>
    <MatchConstraints value={data?.spec?.matchResources} title="Binding Match Constraints" />
    <div className="text-xs text-theme-text-tertiary">
      The effective scope is the intersection of this binding’s match constraints and the policy’s match constraints.
    </div>
  </>
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
          <Property label="Namespace" value={reference.namespace ?? PARAM_NAMESPACE_UNSET} />
        </>}
        {reference.mode === 'selector' && (
          <>
            <Property label="Selector" value={<LabelSelectorDisplay selector={reference.selector} />} />
            <Property label="Namespace" value={reference.namespace ?? PARAM_NAMESPACE_UNSET} />
          </>
        )}
        <Property label="Parameter Not Found" value={reference.parameterNotFoundAction ?? 'Not specified'} />
      </PropertyList>
      <div className="mt-2 text-xs text-theme-text-tertiary">
        Parameter availability is not checked; Radar does not discover or enumerate arbitrary parameter resources.
      </div>
    </>
  )
}
