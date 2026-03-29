import { Shield } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner } from '../../ui/drawer-components'

interface WebhookConfigRendererProps {
  data: any
  isMutating?: boolean
}

function getOperationColor(op: string): string {
  if (op === 'CREATE') return 'bg-green-500/20 text-green-400'
  if (op === 'UPDATE') return 'bg-yellow-500/20 text-yellow-400'
  if (op === 'DELETE') return 'bg-red-500/20 text-red-400'
  if (op === '*') return 'bg-red-500/20 text-red-400'
  if (op === 'CONNECT') return 'bg-blue-500/20 text-blue-400'
  return 'bg-theme-elevated text-theme-text-secondary'
}

function getSideEffectsColor(se: string): string {
  if (se === 'None' || se === 'NoneOnDryRun') return 'bg-green-500/20 text-green-400'
  return 'bg-amber-500/20 text-amber-400'
}

export function WebhookConfigRenderer({ data, isMutating }: WebhookConfigRendererProps) {
  const webhooks = data.webhooks || []
  const hasFailPolicy = webhooks.some((w: any) => w.failurePolicy === 'Fail')
  const allIgnore = webhooks.length > 0 && webhooks.every((w: any) => w.failurePolicy === 'Ignore')

  return (
    <>
      {hasFailPolicy && (
        <AlertBanner
          variant="warning"
          title="Blocking Webhook"
          message="One or more webhooks use failurePolicy: Fail. API requests will be rejected if the webhook is unavailable."
        />
      )}

      <Section title="Overview" icon={Shield}>
        <PropertyList>
          <Property label="Webhooks" value={webhooks.length} />
          <Property
            label="Failure Policy"
            value={
              hasFailPolicy ? (
                <span className="badge-sm status-unhealthy">Fail</span>
              ) : allIgnore ? (
                <span className="badge-sm bg-theme-elevated text-theme-text-secondary">Ignore</span>
              ) : (
                'Mixed'
              )
            }
          />
        </PropertyList>
      </Section>

      <Section title={`Webhooks (${webhooks.length})`} icon={Shield} defaultExpanded>
        <div className="space-y-3">
          {webhooks.map((wh: any, i: number) => {
            const svc = wh.clientConfig?.service
            const url = wh.clientConfig?.url
            const target = svc
              ? `Service: ${svc.namespace}/${svc.name}${svc.port ? `:${svc.port}` : ''}${svc.path || ''}`
              : url
                ? `URL: ${url}`
                : 'No target'

            const nsLabels = wh.namespaceSelector?.matchLabels
            const objLabels = wh.objectSelector?.matchLabels

            return (
              <div key={i} className="card-inner-lg">
                <div className="text-sm font-medium text-theme-text-primary">{wh.name}</div>
                <div className="text-xs text-theme-text-secondary mt-0.5">{target}</div>

                {/* Policy badges */}
                <div className="flex flex-wrap gap-1.5 mt-2">
                  <span className={clsx('badge-sm', wh.failurePolicy === 'Fail' ? 'bg-red-500/20 text-red-400' : 'bg-theme-elevated text-theme-text-secondary')}>
                    {wh.failurePolicy || 'Fail'}
                  </span>
                  {wh.sideEffects && (
                    <span className={clsx('badge-sm', getSideEffectsColor(wh.sideEffects))}>
                      {wh.sideEffects}
                    </span>
                  )}
                  <span className="badge-sm bg-theme-elevated text-theme-text-secondary">
                    {wh.timeoutSeconds ?? 10}s
                  </span>
                  {wh.matchPolicy && (
                    <span className="badge-sm bg-theme-elevated text-theme-text-secondary">
                      {wh.matchPolicy}
                    </span>
                  )}
                  {isMutating && wh.reinvocationPolicy && (
                    <span className="badge-sm bg-theme-elevated text-theme-text-secondary">
                      {wh.reinvocationPolicy}
                    </span>
                  )}
                </div>

                {/* Rules */}
                {wh.rules && wh.rules.length > 0 && (
                  <div className="mt-2 space-y-2">
                    {wh.rules.map((rule: any, ri: number) => (
                      <div key={ri} className="flex flex-wrap items-center gap-1 text-xs">
                        {(rule.operations || []).map((op: string) => (
                          <span key={op} className={clsx('px-1.5 py-0.5 rounded', getOperationColor(op))}>{op}</span>
                        ))}
                        <span className="text-theme-text-tertiary">on</span>
                        {(rule.resources || []).map((r: string) => (
                          <span key={r} className="px-1.5 py-0.5 rounded bg-purple-500/20 text-purple-400">{r}</span>
                        ))}
                        {(rule.apiGroups || []).map((g: string) => (
                          <span key={g} className="px-1.5 py-0.5 rounded bg-theme-elevated text-theme-text-secondary">{g === '' ? 'core' : g}</span>
                        ))}
                      </div>
                    ))}
                  </div>
                )}

                {/* Selectors */}
                {(nsLabels || objLabels) && (
                  <div className="mt-2 space-y-1">
                    {nsLabels && Object.keys(nsLabels).length > 0 && (
                      <div>
                        <div className="text-xs text-theme-text-tertiary mb-1">Namespace Selector</div>
                        <div className="flex flex-wrap gap-1">
                          {Object.entries(nsLabels).map(([k, v]) => (
                            <span key={k} className="badge-sm bg-theme-elevated text-theme-text-secondary">{k}={String(v)}</span>
                          ))}
                        </div>
                      </div>
                    )}
                    {objLabels && Object.keys(objLabels).length > 0 && (
                      <div>
                        <div className="text-xs text-theme-text-tertiary mb-1">Object Selector</div>
                        <div className="flex flex-wrap gap-1">
                          {Object.entries(objLabels).map(([k, v]) => (
                            <span key={k} className="badge-sm bg-theme-elevated text-theme-text-secondary">{k}={String(v)}</span>
                          ))}
                        </div>
                      </div>
                    )}
                  </div>
                )}
              </div>
            )
          })}
          {webhooks.length === 0 && (
            <div className="text-sm text-theme-text-tertiary">No webhooks defined</div>
          )}
        </div>
      </Section>
    </>
  )
}
