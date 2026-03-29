import { Shield } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, KeyValueBadgeList } from '../../ui/drawer-components'
import {
  getAuthorizationPolicyAction,
  getAuthorizationPolicyRules,
  getAuthorizationPolicySelector,
} from '../resource-utils-istio'

const actionColors: Record<string, string> = {
  ALLOW: 'bg-green-500/20 text-green-400',
  DENY: 'bg-red-500/20 text-red-400',
  CUSTOM: 'bg-blue-500/20 text-blue-400',
  AUDIT: 'bg-yellow-500/20 text-yellow-400',
}

interface IstioAuthorizationPolicyRendererProps {
  data: any
}

export function IstioAuthorizationPolicyRenderer({ data }: IstioAuthorizationPolicyRendererProps) {
  const action = getAuthorizationPolicyAction(data)
  const rules = getAuthorizationPolicyRules(data)
  const selector = getAuthorizationPolicySelector(data)
  const hasSelector = Object.keys(selector).length > 0

  // Special case: DENY with no rules = deny all
  const isDenyAll = action === 'DENY' && rules.length === 0
  // Special case: ALLOW with no rules = allow nothing (deny all)
  const isAllowNothing = action === 'ALLOW' && rules.length === 0 && !data.spec?.rules

  return (
    <>
      {isDenyAll && (
        <AlertBanner
          variant="error"
          title="Deny All"
          message="This policy denies all traffic to the target workload (DENY action with no rules)."
        />
      )}
      {isAllowNothing && (
        <AlertBanner
          variant="warning"
          title="Allow Nothing"
          message="This policy has ALLOW action but no rules, which means no traffic is allowed."
        />
      )}

      {/* Policy info */}
      <Section title="Authorization Policy" icon={Shield} defaultExpanded>
        <PropertyList>
          <Property label="Action" value={
            <span className={clsx(
              'badge',
              actionColors[action] || 'bg-gray-500/20 text-gray-400'
            )}>
              {action}
            </span>
          } />
          <Property label="Scope" value={hasSelector ? 'Workload-scoped' : 'Namespace-wide'} />
          <Property label="Rules" value={String(rules.length)} />
          {data.spec?.provider?.name && (
            <Property label="Provider" value={data.spec.provider.name} />
          )}
        </PropertyList>
      </Section>

      {/* Selector */}
      {hasSelector && (
        <Section title="Workload Selector" defaultExpanded>
          <KeyValueBadgeList items={selector} />
        </Section>
      )}

      {/* Rules */}
      {rules.length > 0 && (
        <Section title={`Rules (${rules.length})`} defaultExpanded>
          <div className="space-y-3">
            {rules.map((rule: any, i: number) => (
              <div key={i} className="card-inner-lg">
                <div className="text-sm font-medium text-theme-text-primary mb-2">Rule {i + 1}</div>

                {/* From (sources) */}
                {rule.from && rule.from.length > 0 && (
                  <div className="mb-2">
                    <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">From (Sources)</div>
                    {rule.from.map((f: any, fi: number) => (
                      <div key={fi} className="flex flex-wrap gap-1 mb-1">
                        {f.source?.principals?.map((p: string, pi: number) => (
                          <span key={`p-${pi}`} className="badge-sm bg-blue-500/10 text-blue-400">
                            principal: {p}
                          </span>
                        ))}
                        {f.source?.namespaces?.map((n: string, ni: number) => (
                          <span key={`n-${ni}`} className="badge-sm bg-purple-500/10 text-purple-400">
                            namespace: {n}
                          </span>
                        ))}
                        {f.source?.ipBlocks?.map((ip: string, ipi: number) => (
                          <span key={`ip-${ipi}`} className="badge-sm bg-orange-500/10 text-orange-400">
                            IP: {ip}
                          </span>
                        ))}
                        {f.source?.requestPrincipals?.map((rp: string, rpi: number) => (
                          <span key={`rp-${rpi}`} className="badge-sm bg-cyan-500/10 text-cyan-400">
                            reqPrincipal: {rp}
                          </span>
                        ))}
                      </div>
                    ))}
                  </div>
                )}

                {/* To (operations) */}
                {rule.to && rule.to.length > 0 && (
                  <div className="mb-2">
                    <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">To (Operations)</div>
                    {rule.to.map((t: any, ti: number) => (
                      <div key={ti} className="flex flex-wrap gap-1 mb-1">
                        {t.operation?.hosts?.map((h: string, hi: number) => (
                          <span key={`h-${hi}`} className="badge-sm bg-blue-500/10 text-blue-400">
                            host: {h}
                          </span>
                        ))}
                        {t.operation?.ports?.map((p: string, pi: number) => (
                          <span key={`p-${pi}`} className="badge-sm bg-orange-500/10 text-orange-400">
                            port: {p}
                          </span>
                        ))}
                        {t.operation?.methods?.map((m: string, mi: number) => (
                          <span key={`m-${mi}`} className="badge-sm bg-green-500/10 text-green-400">
                            method: {m}
                          </span>
                        ))}
                        {t.operation?.paths?.map((p: string, pi: number) => (
                          <span key={`path-${pi}`} className="badge-sm bg-purple-500/10 text-purple-400">
                            path: {p}
                          </span>
                        ))}
                      </div>
                    ))}
                  </div>
                )}

                {/* When (conditions) */}
                {rule.when && rule.when.length > 0 && (
                  <div>
                    <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">When (Conditions)</div>
                    {rule.when.map((w: any, wi: number) => (
                      <div key={wi} className="flex flex-wrap gap-1 mb-1 text-xs">
                        <span className="text-theme-text-secondary font-medium">{w.key}</span>
                        {w.values && (
                          <span className="text-theme-text-tertiary">
                            = [{w.values.join(', ')}]
                          </span>
                        )}
                        {w.notValues && (
                          <span className="text-red-400">
                            != [{w.notValues.join(', ')}]
                          </span>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={data.status?.conditions || []} />
    </>
  )
}
