import { Section } from '../../ui/drawer-components'

export interface FluxHealthCheckExpr {
  apiVersion?: string
  kind?: string
  current?: string
  inProgress?: string
  failed?: string
}

// FluxHealthCheckExprs lists spec.healthCheckExprs (Kustomization; HelmRelease
// from Flux 2.8). Flux records no result per expression, so only the
// expressions are shown; the Ready condition names what failed.
export function FluxHealthCheckExprs({ exprs }: { exprs?: FluxHealthCheckExpr[] }) {
  if (!exprs || exprs.length === 0) return null
  return (
    <Section title="Health checks (expressions)" defaultExpanded={true}>
      <div className="space-y-2">
        {exprs.map((expr, i) => (
          <div key={i} className="card-inner text-sm">
            <div>
              <span className="text-theme-text-primary">{expr.kind || '*'}</span>
              {expr.apiVersion && <span className="ml-1 text-theme-text-tertiary">({expr.apiVersion})</span>}
            </div>
            {(['current', 'inProgress', 'failed'] as const).map((field) =>
              expr[field] ? (
                <div key={field} className="mt-0.5 flex gap-2 text-xs">
                  <span className="w-20 shrink-0 text-theme-text-tertiary">{field}</span>
                  <code className="min-w-0 break-all font-mono text-theme-text-secondary">{expr[field]}</code>
                </div>
              ) : null,
            )}
          </div>
        ))}
      </div>
    </Section>
  )
}
