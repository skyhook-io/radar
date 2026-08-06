// Shared building blocks for the modern Kyverno CEL policy family
// (policies.kyverno.io). Each of the five policy kinds gets its own thin
// renderer; everything they genuinely have in common lives here so the
// enforcement-posture answer can't drift between them.

import { Shield, Filter, Code, AlertTriangle } from 'lucide-react'
import { Section, PropertyList, Property, LabelSelectorDisplay, AlertBanner } from '../../ui/drawer-components'
import {
  formatKyvernoResourceRule,
  getKyvernoEnforcementPosture,
  getKyvernoExcludeResourceRules,
  getKyvernoFailurePolicy,
  getKyvernoMatchConditions,
  getKyvernoNamespaceSelector,
  getKyvernoObjectSelector,
  getKyvernoPolicyReady,
  getKyvernoPolicyStatusMessage,
  getKyvernoResourceRules,
  getKyvernoVariables,
  isKyvernoAdmissionEnabled,
  isKyvernoBackgroundEnabled,
  type KyvernoNamedExpression,
  type KyvernoPolicyFamily,
} from '../resource-utils-kyverno-modern'

const POSTURE_TONE: Record<string, string> = {
  healthy: 'status-healthy',
  degraded: 'status-degraded',
  alert: 'status-alert',
  unhealthy: 'status-unhealthy',
  neutral: 'status-neutral',
  unknown: 'status-unknown',
}

/**
 * The answer to the operator's first question — "does this block, and what
 * does it match?" — rendered above everything else.
 *
 * It states the EFFECTIVE posture, not the declared one. A policy declaring
 * `validationActions: [Deny]` with admission evaluation disabled blocks
 * nothing, and showing it as a red "Deny" badge would report a cluster as
 * protected when it isn't.
 */
export function KyvernoPostureHeader({
  data,
  family,
}: {
  data: any
  family: KyvernoPolicyFamily | undefined
}) {
  const posture = getKyvernoEnforcementPosture(data, family)
  const ready = getKyvernoPolicyReady(data)
  const statusMessage = getKyvernoPolicyStatusMessage(data)
  const rules = getKyvernoResourceRules(data)

  return (
    <div className="space-y-2">
      {/* A policy that failed to compile enforces nothing whatever its spec
          says — the most common "why isn't my policy working" cause, so it
          outranks the posture badge. */}
      {ready === false && (
        <AlertBanner
          variant="error"
          icon={AlertTriangle}
          title="Policy not ready"
          message={
            statusMessage ||
            'The controller has not accepted this policy. It is not being enforced — check the CEL expressions for compile errors.'
          }
        />
      )}

      <div className="card-inner-lg">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-xs uppercase tracking-wide text-theme-text-tertiary">Enforcement</span>
          <span className={`badge ${POSTURE_TONE[posture.level] || 'status-unknown'}`}>{posture.label}</span>
          {posture.summary && <span className="text-xs text-theme-text-secondary">{posture.summary}</span>}
        </div>

        {posture.note && <div className="mt-2 text-xs text-theme-text-secondary">{posture.note}</div>}

        {rules.length > 0 && (
          <div className="mt-3">
            <div className="text-xs uppercase tracking-wide text-theme-text-tertiary mb-1">Applies to</div>
            <div className="space-y-1">
              {rules.map((rule, i) => (
                <div key={i} className="text-xs font-mono text-theme-text-primary break-all">
                  {formatKyvernoResourceRule(rule)}
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

/**
 * Match scope beyond the headline resourceRules — exclusions and selectors.
 * The header already shows what the policy applies to; this section is the
 * detail an operator opens when the headline doesn't explain why a given
 * resource was or wasn't caught.
 */
export function KyvernoMatchScopeSection({ data }: { data: any }) {
  const excludeRules = getKyvernoExcludeResourceRules(data)
  const namespaceSelector = getKyvernoNamespaceSelector(data)
  const objectSelector = getKyvernoObjectSelector(data)
  const matchConditions = getKyvernoMatchConditions(data)

  const hasSelectors =
    (namespaceSelector && Object.keys(namespaceSelector).length > 0) ||
    (objectSelector && Object.keys(objectSelector).length > 0)

  if (excludeRules.length === 0 && !hasSelectors && matchConditions.length === 0) return null

  return (
    <Section title="Match Scope" icon={Filter}>
      <PropertyList>
        {namespaceSelector && Object.keys(namespaceSelector).length > 0 && (
          <Property label="Namespace Selector" value={<LabelSelectorDisplay selector={namespaceSelector} inline />} />
        )}
        {objectSelector && Object.keys(objectSelector).length > 0 && (
          <Property label="Object Selector" value={<LabelSelectorDisplay selector={objectSelector} inline />} />
        )}
      </PropertyList>

      {excludeRules.length > 0 && (
        <div className="mt-3">
          <div className="text-xs uppercase tracking-wide text-theme-text-tertiary mb-1">Excluded</div>
          <div className="space-y-1">
            {excludeRules.map((rule, i) => (
              <div key={i} className="text-xs font-mono text-theme-text-secondary break-all">
                {formatKyvernoResourceRule(rule)}
              </div>
            ))}
          </div>
        </div>
      )}

      {matchConditions.length > 0 && (
        <div className="mt-3">
          <div className="text-xs uppercase tracking-wide text-theme-text-tertiary mb-1">
            Match Conditions ({matchConditions.length})
          </div>
          <KyvernoExpressionList items={matchConditions} />
        </div>
      )}
    </Section>
  )
}

/** A list of named CEL expressions in a mono block. */
export function KyvernoExpressionList({ items }: { items: KyvernoNamedExpression[] }) {
  if (items.length === 0) return null
  return (
    <div className="space-y-2">
      {items.map((item, i) => (
        <div key={i} className="card-inner">
          {item.name && <div className="text-xs font-medium text-theme-text-primary mb-1">{item.name}</div>}
          <pre className="text-xs text-theme-text-secondary font-mono whitespace-pre-wrap break-all">
            {item.expression}
          </pre>
        </div>
      ))}
    </div>
  )
}

export function KyvernoVariablesSection({ data }: { data: any }) {
  const variables = getKyvernoVariables(data)
  if (variables.length === 0) return null
  return (
    <Section title={`Variables (${variables.length})`} icon={Code} defaultExpanded={false}>
      <KyvernoExpressionList items={variables} />
    </Section>
  )
}

/**
 * The evaluation/webhook knobs shared by the four admission-shaped kinds.
 * DeletingPolicy has none of these — it is scheduled, not admission-driven —
 * and deliberately does not render this section.
 */
export function KyvernoEvaluationSection({ data }: { data: any }) {
  const failurePolicy = getKyvernoFailurePolicy(data)
  return (
    <Section title="Evaluation" icon={Shield} defaultExpanded={false}>
      <PropertyList>
        <Property label="Admission" value={isKyvernoAdmissionEnabled(data) ? 'Enabled' : 'Disabled'} />
        <Property label="Background Scans" value={isKyvernoBackgroundEnabled(data) ? 'Enabled' : 'Disabled'} />
        {failurePolicy && (
          <Property
            label="Failure Policy"
            value={
              <span className="flex items-center gap-2">
                <span className={`badge ${failurePolicy === 'Ignore' ? 'status-degraded' : 'status-neutral'}`}>
                  {failurePolicy}
                </span>
                <span className="text-xs text-theme-text-tertiary">
                  {failurePolicy === 'Ignore'
                    ? 'requests are admitted when the engine itself errors'
                    : 'requests are rejected when the engine itself errors'}
                </span>
              </span>
            }
          />
        )}
      </PropertyList>
    </Section>
  )
}
