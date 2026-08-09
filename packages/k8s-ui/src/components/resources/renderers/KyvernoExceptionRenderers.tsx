// PolicyException and CleanupPolicy renderers.
//
// PolicyException is the record of who was let off the hook — the most
// operationally interesting Kyverno object after the policy itself. One
// renderer serves both API families; the accessors it calls are
// group-qualified, so the same component reads whichever shape it is handed.

import { ShieldOff, Filter, Clock, Trash2, AlertTriangle, ScrollText } from 'lucide-react'
import {
  Section,
  PropertyList,
  Property,
  ConditionsSection,
  AlertBanner,
  LabelSelectorDisplay,
} from '../../ui/drawer-components'
import { KyvernoExpressionList } from './KyvernoPolicyShared'
import {
  getKyvernoCleanupConditions,
  getKyvernoCleanupExclude,
  getKyvernoCleanupLastExecution,
  getKyvernoCleanupMatch,
  getKyvernoCleanupMatchedKinds,
  getKyvernoCleanupPropagationPolicy,
  getKyvernoCleanupSchedule,
  getKyvernoExceptedPolicies,
  getKyvernoExceptionLegacyConditions,
  getKyvernoExceptionMatch,
  getKyvernoExceptionMatchConditions,
  getKyvernoExceptionReportResult,
  isBlanketKyvernoException,
  isModernKyvernoPolicyException,
} from '../resource-utils-kyverno-exceptions'

/**
 * Renders the legacy any/all match block. Each entry carries a
 * `resources` selector with kinds/names/namespaces/selector.
 */
function LegacyMatchBlock({ match }: { match: any }) {
  const blocks: Array<{ operator: string; entries: any[] }> = []
  if (Array.isArray(match?.any)) blocks.push({ operator: 'any', entries: match.any })
  if (Array.isArray(match?.all)) blocks.push({ operator: 'all', entries: match.all })
  if (blocks.length === 0) return null

  return (
    <div className="space-y-2">
      {blocks.map((block) => (
        <div key={block.operator}>
          <div className="text-xs uppercase tracking-wide text-theme-text-tertiary mb-1">Match {block.operator}</div>
          <div className="space-y-2">
            {block.entries.map((entry, i) => {
              const resources = entry?.resources || {}
              return (
                <div key={i} className="card-inner">
                  <PropertyList>
                    {Array.isArray(resources.kinds) && resources.kinds.length > 0 && (
                      <Property label="Kinds" value={resources.kinds.join(', ')} />
                    )}
                    {Array.isArray(resources.names) && resources.names.length > 0 && (
                      <Property label="Names" value={resources.names.join(', ')} />
                    )}
                    {Array.isArray(resources.namespaces) && resources.namespaces.length > 0 && (
                      <Property label="Namespaces" value={resources.namespaces.join(', ')} />
                    )}
                    {resources.selector && (
                      <Property label="Selector" value={<LabelSelectorDisplay selector={resources.selector} inline />} />
                    )}
                    {resources.namespaceSelector && (
                      <Property
                        label="Namespace Selector"
                        value={<LabelSelectorDisplay selector={resources.namespaceSelector} inline />}
                      />
                    )}
                  </PropertyList>
                </div>
              )
            })}
          </div>
        </div>
      ))}
    </div>
  )
}

export function KyvernoPolicyExceptionRenderer({ data }: { data: any }) {
  const isModern = isModernKyvernoPolicyException(data)
  const excepted = getKyvernoExceptedPolicies(data)
  const blanket = isBlanketKyvernoException(data)
  const matchConditions = getKyvernoExceptionMatchConditions(data)
  const legacyMatch = getKyvernoExceptionMatch(data)
  const legacyConditions = getKyvernoExceptionLegacyConditions(data)
  const reportResult = getKyvernoExceptionReportResult(data)

  return (
    <div className="space-y-4">
      {/* An exception naming no policy exempts everything in its match scope.
          That is the widest exemption expressible here and the operator has to
          see it before anything else. */}
      {blanket && (
        <AlertBanner
          variant="warning"
          icon={AlertTriangle}
          title="Blanket exception"
          message="This exception names no specific policy, so every policy matching its scope is bypassed."
        />
      )}

      <div className="card-inner-lg">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-xs uppercase tracking-wide text-theme-text-tertiary">Exempts</span>
          <span className={`badge ${blanket ? 'status-alert' : 'status-neutral'}`}>
            {blanket ? 'All matching policies' : `${excepted.length} ${excepted.length === 1 ? 'policy' : 'policies'}`}
          </span>
          <span className="badge status-neutral">{isModern ? 'policies.kyverno.io' : 'kyverno.io'}</span>
        </div>
        {/* Kyverno has no expiry field on either shape — the upstream pattern
            is a companion cleanup policy — so an exception persists until
            somebody deletes it. */}
        <div className="mt-2 text-xs text-theme-text-secondary">
          Exceptions do not expire; this one applies until it is deleted.
        </div>
      </div>

      {excepted.length > 0 && (
        <Section title={`Excepted Policies (${excepted.length})`} icon={ShieldOff}>
          <div className="space-y-2">
            {excepted.map((policy, i) => (
              <div key={i} className="card-inner">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-sm font-medium text-theme-text-primary">{policy.name}</span>
                  {policy.kind && <span className="badge status-neutral">{policy.kind}</span>}
                </div>
                {policy.ruleNames.length > 0 ? (
                  <div className="mt-1 flex items-center gap-1 flex-wrap">
                    <span className="text-xs text-theme-text-tertiary">Rules:</span>
                    {policy.ruleNames.map((rule) => (
                      <span key={rule} className="badge status-neutral">
                        {rule}
                      </span>
                    ))}
                  </div>
                ) : (
                  !isModern && <div className="mt-1 text-xs text-theme-text-tertiary">All rules in this policy</div>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {reportResult && (
        <Section title="Reporting" icon={ScrollText}>
          <PropertyList>
            <Property
              label="Report Result"
              value={
                <span className="flex items-center gap-2">
                  <span className="badge status-neutral">{reportResult}</span>
                  <span className="text-xs text-theme-text-tertiary">how excepted resources appear in reports</span>
                </span>
              }
            />
          </PropertyList>
        </Section>
      )}

      {legacyMatch && (
        <Section title="Match Scope" icon={Filter}>
          <LegacyMatchBlock match={legacyMatch} />
        </Section>
      )}

      {matchConditions.length > 0 && (
        <Section title={`Match Conditions (${matchConditions.length})`} icon={Filter}>
          <KyvernoExpressionList items={matchConditions} />
        </Section>
      )}

      {legacyConditions && (
        <Section title="Conditions" icon={Filter} defaultExpanded={false}>
          <pre className="text-xs text-theme-text-secondary font-mono whitespace-pre-wrap break-all">
            {JSON.stringify(legacyConditions, null, 2)}
          </pre>
        </Section>
      )}

      <ConditionsSection conditions={data?.status?.conditions} />
    </div>
  )
}

/**
 * CleanupPolicy / ClusterCleanupPolicy (kyverno.io/v2), deprecated in Kyverno
 * 1.18 in favour of DeletingPolicy. Rendered because the installed base runs
 * it today and "what deleted my resource?" is an expensive question to answer
 * without it.
 */
export function KyvernoCleanupPolicyRenderer({ data }: { data: any }) {
  const schedule = getKyvernoCleanupSchedule(data)
  const kinds = getKyvernoCleanupMatchedKinds(data)
  const lastExecution = getKyvernoCleanupLastExecution(data)
  const propagation = getKyvernoCleanupPropagationPolicy(data)
  const match = getKyvernoCleanupMatch(data)
  const exclude = getKyvernoCleanupExclude(data)
  const conditions = getKyvernoCleanupConditions(data)

  return (
    <div className="space-y-4">
      <div className="card-inner-lg">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-xs uppercase tracking-wide text-theme-text-tertiary">Deletes</span>
          <span className="badge status-alert">{kinds.length > 0 ? kinds.join(', ') : 'matched resources'}</span>
          {schedule ? (
            <span className="text-xs text-theme-text-secondary">
              on schedule <span className="font-mono">{schedule}</span>
            </span>
          ) : (
            <span className="text-xs text-theme-text-secondary">no schedule set — this policy will not run</span>
          )}
        </div>
        <div className="mt-2 text-xs text-theme-text-secondary">
          Deprecated in Kyverno 1.18; DeletingPolicy (policies.kyverno.io) is its replacement.
        </div>
      </div>

      <Section title="Schedule" icon={Clock}>
        <PropertyList>
          <Property label="Cron" value={schedule ? <span className="font-mono">{schedule}</span> : 'Not set'} />
          {propagation && <Property label="Deletion Propagation" value={propagation} />}
          {lastExecution && <Property label="Last Execution" value={lastExecution} />}
        </PropertyList>
      </Section>

      {match && (
        <Section title="Match Scope" icon={Filter}>
          <LegacyMatchBlock match={match} />
        </Section>
      )}

      {exclude && (
        <Section title="Excluded" icon={Filter} defaultExpanded={false}>
          <LegacyMatchBlock match={exclude} />
        </Section>
      )}

      {conditions ? (
        <Section title="Deletion Conditions" icon={Trash2}>
          <pre className="text-xs text-theme-text-secondary font-mono whitespace-pre-wrap break-all">
            {JSON.stringify(conditions, null, 2)}
          </pre>
        </Section>
      ) : (
        <AlertBanner
          variant="warning"
          icon={Trash2}
          title="No deletion conditions"
          message="Every resource matched by this policy is deleted when the schedule fires."
        />
      )}

      <ConditionsSection conditions={data?.status?.conditions} />
    </div>
  )
}
