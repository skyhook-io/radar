import { Shield, ArrowDownToLine, ArrowUpFromLine, Ban, Lock } from 'lucide-react'
import { Section, LabelSelectorDisplay } from '../../ui/drawer-components'

interface CiliumNetworkPolicyRendererProps {
  data: any
}

export function CiliumNetworkPolicyRenderer({ data }: CiliumNetworkPolicyRendererProps) {
  const spec = data.spec || data.specs || {}
  const endpointSelector = spec.endpointSelector || {}
  const ingress: any[] | undefined = spec.ingress
  const ingressDeny: any[] | undefined = spec.ingressDeny
  const egress: any[] | undefined = spec.egress
  const egressDeny: any[] | undefined = spec.egressDeny
  const description = spec.description || ''
  const enableDefaultDeny = spec.enableDefaultDeny

  return (
    <>
      <Section title="Target" icon={Shield}>
        {description && (
          <p className="text-xs text-theme-text-secondary mb-2">{description}</p>
        )}
        <div className="mt-2">
          <div className="text-xs text-theme-text-tertiary mb-1">Endpoint Selector</div>
          <LabelSelectorDisplay selector={endpointSelector} emptyText="All endpoints in namespace" />
        </div>
        {spec.nodeSelector && (
          <div className="mt-2">
            <div className="text-xs text-theme-text-tertiary mb-1">Node Selector</div>
            <LabelSelectorDisplay selector={spec.nodeSelector} emptyText="All nodes" />
          </div>
        )}
        {enableDefaultDeny && (
          <div className="mt-2">
            <div className="text-xs text-theme-text-tertiary mb-1">Default Deny</div>
            <div className="flex flex-wrap gap-1">
              {enableDefaultDeny.ingress !== undefined && (
                <span className="badge bg-theme-elevated text-theme-text-secondary">
                  Ingress: {enableDefaultDeny.ingress ? 'Yes' : 'No'}
                </span>
              )}
              {enableDefaultDeny.egress !== undefined && (
                <span className="badge bg-theme-elevated text-theme-text-secondary">
                  Egress: {enableDefaultDeny.egress ? 'Yes' : 'No'}
                </span>
              )}
            </div>
          </div>
        )}
      </Section>

      {ingress && ingress.length > 0 && (
        <Section title="Ingress Allow" icon={ArrowDownToLine} defaultExpanded>
          <div className="space-y-3">
            {ingress.map((rule: any, i: number) => (
              <CiliumRuleCard key={i} rule={rule} />
            ))}
          </div>
        </Section>
      )}

      {ingressDeny && ingressDeny.length > 0 && (
        <Section title="Ingress Deny" icon={Ban} defaultExpanded>
          <div className="space-y-3">
            {ingressDeny.map((rule: any, i: number) => (
              <CiliumRuleCard key={i} rule={rule} />
            ))}
          </div>
        </Section>
      )}

      {egress && egress.length > 0 && (
        <Section title="Egress Allow" icon={ArrowUpFromLine} defaultExpanded>
          <div className="space-y-3">
            {egress.map((rule: any, i: number) => (
              <CiliumRuleCard key={i} rule={rule} />
            ))}
          </div>
        </Section>
      )}

      {egressDeny && egressDeny.length > 0 && (
        <Section title="Egress Deny" icon={Ban} defaultExpanded>
          <div className="space-y-3">
            {egressDeny.map((rule: any, i: number) => (
              <CiliumRuleCard key={i} rule={rule} />
            ))}
          </div>
        </Section>
      )}
    </>
  )
}

function CiliumRuleCard({ rule }: { rule: any }) {
  const fromEndpoints: any[] = rule.fromEndpoints || []
  const fromEntities: string[] = rule.fromEntities || []
  const fromCIDR: string[] = rule.fromCIDR || []
  const fromCIDRSet: any[] = rule.fromCIDRSet || []
  const fromFQDNs: any[] = rule.fromFQDNs || []
  const fromNodes: any[] = rule.fromNodes || []
  const fromGroups: any[] = rule.fromGroups || []
  const toEndpoints: any[] = rule.toEndpoints || []
  const toEntities: string[] = rule.toEntities || []
  const toCIDR: string[] = rule.toCIDR || []
  const toCIDRSet: any[] = rule.toCIDRSet || []
  const toFQDNs: any[] = rule.toFQDNs || []
  const toServices: any[] = rule.toServices || []
  const toNodes: any[] = rule.toNodes || []
  const toGroups: any[] = rule.toGroups || []
  const toPorts: any[] = rule.toPorts || []
  const icmps: any[] = rule.icmps || []
  const authentication = rule.authentication

  const hasFrom = fromEndpoints.length > 0 || fromEntities.length > 0 || fromCIDR.length > 0 ||
    fromCIDRSet.length > 0 || fromFQDNs.length > 0 || fromNodes.length > 0 || fromGroups.length > 0
  const hasTo = toEndpoints.length > 0 || toEntities.length > 0 || toCIDR.length > 0 ||
    toCIDRSet.length > 0 || toFQDNs.length > 0 || toServices.length > 0 || toNodes.length > 0 || toGroups.length > 0
  const hasRules = hasFrom || hasTo || toPorts.length > 0 || icmps.length > 0

  return (
    <div className="card-inner-lg">
      {authentication && (
        <div className="mb-2 flex items-center gap-1.5">
          <Lock className="h-3 w-3 text-yellow-400" />
          <span className="text-xs text-yellow-400">Authentication: {authentication.mode || 'required'}</span>
        </div>
      )}

      {fromEndpoints.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">From Endpoints</div>
          <div className="space-y-1">
            {fromEndpoints.map((ep: any, j: number) => (
              <LabelSelectorDisplay key={j} selector={ep} emptyText="all endpoints" inline />
            ))}
          </div>
        </div>
      )}

      {fromEntities.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">From Entities</div>
          <div className="flex flex-wrap gap-1">
            {fromEntities.map((e, j) => (
              <span key={j} className="badge status-purple">{e}</span>
            ))}
          </div>
        </div>
      )}

      {(fromCIDR.length > 0 || fromCIDRSet.length > 0) && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">From CIDR</div>
          <div className="flex flex-wrap gap-1">
            {fromCIDR.map((c, j) => (
              <span key={j} className="badge bg-theme-elevated text-theme-text-secondary">{c}</span>
            ))}
            {fromCIDRSet.map((cs: any, j: number) => (
              <span key={`set-${j}`} className="badge bg-theme-elevated text-theme-text-secondary">{cs.cidr}</span>
            ))}
          </div>
        </div>
      )}

      {fromFQDNs.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">From FQDNs</div>
          <div className="flex flex-wrap gap-1">
            {fromFQDNs.map((fqdn: any, j: number) => (
              <span key={j} className="badge bg-theme-elevated text-theme-text-secondary">
                {fqdn.matchName || fqdn.matchPattern || JSON.stringify(fqdn)}
              </span>
            ))}
          </div>
        </div>
      )}

      {fromNodes.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">From Nodes</div>
          <div className="space-y-1">
            {fromNodes.map((node: any, j: number) => (
              <LabelSelectorDisplay key={j} selector={node} />
            ))}
          </div>
        </div>
      )}

      {fromGroups.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">From Groups</div>
          <div className="flex flex-wrap gap-1">
            {fromGroups.map((g: any, j: number) => (
              <CloudGroupEntry key={j} group={g} />
            ))}
          </div>
        </div>
      )}

      {toEndpoints.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">To Endpoints</div>
          <div className="space-y-1">
            {toEndpoints.map((ep: any, j: number) => (
              <LabelSelectorDisplay key={j} selector={ep} emptyText="all endpoints" inline />
            ))}
          </div>
        </div>
      )}

      {toEntities.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">To Entities</div>
          <div className="flex flex-wrap gap-1">
            {toEntities.map((e, j) => (
              <span key={j} className="badge status-purple">{e}</span>
            ))}
          </div>
        </div>
      )}

      {(toCIDR.length > 0 || toCIDRSet.length > 0) && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">To CIDR</div>
          <div className="flex flex-wrap gap-1">
            {toCIDR.map((c, j) => (
              <span key={j} className="badge bg-theme-elevated text-theme-text-secondary">{c}</span>
            ))}
            {toCIDRSet.map((cs: any, j: number) => (
              <span key={`set-${j}`} className="badge bg-theme-elevated text-theme-text-secondary">{cs.cidr}</span>
            ))}
          </div>
        </div>
      )}

      {toFQDNs.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">To FQDNs</div>
          <div className="flex flex-wrap gap-1">
            {toFQDNs.map((fqdn: any, j: number) => (
              <span key={j} className="badge bg-theme-elevated text-theme-text-secondary">
                {fqdn.matchName || fqdn.matchPattern || JSON.stringify(fqdn)}
              </span>
            ))}
          </div>
        </div>
      )}

      {toServices.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">To Services</div>
          <div className="flex flex-wrap gap-1">
            {toServices.map((svc: any, j: number) => {
              const k8s = svc.k8sService
              const sel = svc.k8sServiceSelector
              if (k8s) {
                const ns = k8s.namespace ? `${k8s.namespace}/` : ''
                return (
                  <span key={j} className="badge bg-theme-elevated text-theme-text-secondary">
                    {ns}{k8s.serviceName}
                  </span>
                )
              }
              if (sel) {
                return <LabelSelectorDisplay key={j} selector={sel.selector || sel} />
              }
              return (
                <span key={j} className="badge bg-theme-elevated text-theme-text-secondary">
                  {JSON.stringify(svc)}
                </span>
              )
            })}
          </div>
        </div>
      )}

      {toNodes.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">To Nodes</div>
          <div className="space-y-1">
            {toNodes.map((node: any, j: number) => (
              <LabelSelectorDisplay key={j} selector={node} />
            ))}
          </div>
        </div>
      )}

      {toGroups.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">To Groups</div>
          <div className="flex flex-wrap gap-1">
            {toGroups.map((g: any, j: number) => (
              <CloudGroupEntry key={j} group={g} />
            ))}
          </div>
        </div>
      )}

      {toPorts.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">Ports</div>
          <div className="flex flex-wrap gap-1">
            {toPorts.map((p: any, j: number) => {
              const ports = p.ports || []
              return ports.map((port: any, k: number) => (
                <span key={`${j}-${k}`} className="badge bg-theme-elevated text-theme-text-secondary">
                  {port.protocol || 'TCP'}/{port.port}
                </span>
              ))
            })}
          </div>
          {toPorts.some((p: any) => p.rules) && (
            <div className="mt-2">
              {toPorts.map((p: any, j: number) => {
                if (!p.rules) return null
                return <L7Rules key={j} rules={p.rules} ports={p.ports} />
              })}
            </div>
          )}
        </div>
      )}

      {icmps.length > 0 && (
        <div className="mb-2">
          <div className="text-xs text-theme-text-tertiary mb-1">ICMP</div>
          <div className="flex flex-wrap gap-1">
            {icmps.map((icmp: any, j: number) => {
              const fields = icmp.fields || []
              return fields.map((f: any, k: number) => (
                <span key={`${j}-${k}`} className="badge bg-theme-elevated text-theme-text-secondary">
                  {f.family || 'IPv4'} type {f.type}
                </span>
              ))
            })}
          </div>
        </div>
      )}

      {!hasRules && (
        <div className="text-xs text-theme-text-tertiary">Allow all</div>
      )}
    </div>
  )
}

function L7Rules({ rules, ports }: { rules: any; ports?: any[] }) {
  const portLabel = ports?.map((p: any) => `${p.protocol || 'TCP'}/${p.port}`).join(', ')
  const httpRules: any[] = rules.http || []
  const dnsRules: any[] = rules.dns || []
  const kafkaRules: any[] = rules.kafka || []

  if (httpRules.length === 0 && dnsRules.length === 0 && kafkaRules.length === 0) return null

  return (
    <div className="card-inner text-xs space-y-2">
      {portLabel && (
        <div className="text-theme-text-tertiary">L7 rules for {portLabel}</div>
      )}
      {httpRules.length > 0 && (
        <div>
          <div className="text-theme-text-tertiary mb-1">HTTP</div>
          <div className="space-y-1">
            {httpRules.map((r: any, i: number) => (
              <div key={i} className="flex flex-wrap items-center gap-1">
                {r.method && (
                  <span className="badge badge-sm status-blue">
                    {r.method}
                  </span>
                )}
                {r.path && (
                  <span className="text-theme-text-secondary font-mono">{r.path}</span>
                )}
                {r.headers && r.headers.map((h: any, hi: number) => (
                  <span key={hi} className="badge badge-sm bg-theme-elevated text-theme-text-secondary">
                    {Object.entries(h).map(([k, v]) => `${k}: ${v}`).join(', ')}
                  </span>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}
      {dnsRules.length > 0 && (
        <div>
          <div className="text-theme-text-tertiary mb-1">DNS</div>
          <div className="flex flex-wrap gap-1">
            {dnsRules.map((r: any, i: number) => (
              <span key={i} className="badge badge-sm bg-theme-elevated text-theme-text-secondary">
                {r.matchName || r.matchPattern || JSON.stringify(r)}
              </span>
            ))}
          </div>
        </div>
      )}
      {kafkaRules.length > 0 && (
        <div>
          <div className="text-theme-text-tertiary mb-1">Kafka</div>
          <div className="space-y-1">
            {kafkaRules.map((r: any, i: number) => (
              <div key={i} className="flex flex-wrap gap-1">
                {r.apiKey && (
                  <span className="badge badge-sm bg-theme-elevated text-theme-text-secondary">
                    {r.apiKey}
                  </span>
                )}
                {r.topic && (
                  <span className="badge badge-sm bg-theme-elevated text-theme-text-secondary">
                    topic: {r.topic}
                  </span>
                )}
                {r.role && (
                  <span className="badge badge-sm bg-theme-elevated text-theme-text-secondary">
                    role: {r.role}
                  </span>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function CloudGroupEntry({ group }: { group: any }) {
  const aws = group.aws
  if (aws) {
    const parts: string[] = []
    if (aws.region) parts.push(aws.region)
    if (aws.securityGroupsIds) parts.push(...aws.securityGroupsIds)
    if (aws.securityGroupsNames) parts.push(...aws.securityGroupsNames)
    return (
      <span className="badge bg-theme-elevated text-theme-text-secondary">
        AWS: {parts.join(', ') || JSON.stringify(aws)}
      </span>
    )
  }
  return (
    <span className="badge bg-theme-elevated text-theme-text-secondary">
      {JSON.stringify(group)}
    </span>
  )
}
