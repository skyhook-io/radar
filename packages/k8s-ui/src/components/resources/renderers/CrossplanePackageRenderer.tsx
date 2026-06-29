import { Package, Settings, ScrollText, Layers } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import {
  getProviderStatus,
  getProviderPackage,
  getProviderPackagePullPolicy,
  getProviderCurrentRevision,
} from '../resource-utils-crossplane'

/**
 * Shared renderer for Crossplane package kinds — Provider, Function, Configuration.
 *
 * All three share the same `pkg.crossplane.io` package machinery (spec.package,
 * pull policy, revision history, currentRevision in status, Healthy/Installed
 * conditions). The only material difference is that a Configuration's
 * `status.objectRefs` lists the XRDs/Compositions/Functions it installed —
 * surfaced as an extra section.
 */
interface CrossplanePackageRendererProps {
  data: any
  /** Human label for the section header (e.g. "Provider", "Function", "Configuration"). */
  kindLabel: string
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CrossplanePackageRenderer({ data, kindLabel, onNavigate }: CrossplanePackageRendererProps) {
  const status = getProviderStatus(data)
  const pkg = getProviderPackage(data)
  const pullPolicy = getProviderPackagePullPolicy(data)
  const currentRevision = getProviderCurrentRevision(data)
  const conditions = data?.status?.conditions || []

  const installed = conditions.find((c: any) => c.type === 'Installed')
  const healthy = conditions.find((c: any) => c.type === 'Healthy')
  const objectRefs: any[] = data?.status?.objectRefs || []
  const dependsOn: any[] = data?.spec?.dependsOn || []

  return (
    <>
      {installed?.status === 'False' && (
        <AlertBanner
          variant="error"
          title={`${kindLabel} install failed`}
          message={installed.message || installed.reason || 'Package failed to install.'}
        />
      )}
      {installed?.status === 'True' && healthy?.status === 'False' && (
        <AlertBanner
          variant="warning"
          title={`${kindLabel} unhealthy`}
          message={healthy.message || healthy.reason || `${kindLabel} package is installed but its controller is not healthy.`}
        />
      )}

      <Section title={kindLabel} icon={Package} defaultExpanded>
        <PropertyList>
          <Property label="Package" value={<span className="inline-code break-all">{pkg}</span>} />
          {pullPolicy && <Property label="Pull Policy" value={pullPolicy} />}
          {data?.spec?.revisionActivationPolicy && (
            <Property label="Revision Activation" value={data.spec.revisionActivationPolicy} />
          )}
          {typeof data?.spec?.revisionHistoryLimit === 'number' && (
            <Property label="Revision History Limit" value={String(data.spec.revisionHistoryLimit)} />
          )}
          {data?.spec?.skipDependencyResolution !== undefined && (
            <Property label="Skip Dependency Resolution" value={data.spec.skipDependencyResolution ? 'Yes' : 'No'} />
          )}
          <Property label="Status" value={status.text} />
        </PropertyList>
      </Section>

      {(currentRevision || data?.status?.currentIdentifier) && (
        <Section title="Revision" icon={ScrollText} defaultExpanded>
          <PropertyList>
            {currentRevision && (
              <Property label="Current Revision" value={<span className="inline-code break-all">{currentRevision}</span>} />
            )}
            {data?.status?.currentIdentifier && (
              <Property
                label="Current Identifier"
                value={<span className="inline-code break-all">{data.status.currentIdentifier}</span>}
              />
            )}
          </PropertyList>
        </Section>
      )}

      {data?.spec?.runtimeConfigRef?.name && (
        <Section title="Runtime Config" icon={Settings} defaultExpanded>
          <PropertyList>
            <Property
              label="Name"
              value={
                <ResourceLink
                  name={data.spec.runtimeConfigRef.name}
                  kind="deploymentruntimeconfigs"
                  namespace=""
                  onNavigate={onNavigate}
                />
              }
            />
          </PropertyList>
        </Section>
      )}

      {dependsOn.length > 0 && (
        <Section title={`Dependencies (${dependsOn.length})`} icon={Layers} defaultExpanded>
          <div className="space-y-1">
            {dependsOn.map((dep: any, i: number) => {
              const ref = dep.provider || dep.function || dep.configuration
              const depKind = dep.provider ? 'Provider' : dep.function ? 'Function' : dep.configuration ? 'Configuration' : 'Unknown'
              return (
                <div key={i} className="card-inner text-sm flex items-center gap-2 flex-wrap">
                  <span className="badge-sm status-neutral">{depKind}</span>
                  <span className="inline-code break-all">{ref || '-'}</span>
                  {dep.version && (
                    <span className="text-theme-text-tertiary text-xs">version: {dep.version}</span>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {objectRefs.length > 0 && (
        <Section title={`Installed Resources (${objectRefs.length})`} icon={Layers} defaultExpanded>
          <div className="space-y-1">
            {objectRefs.map((ref: any, i: number) => (
              <div key={i} className="card-inner text-sm flex items-center gap-2 flex-wrap">
                <span className="badge-sm status-neutral">{ref.kind || 'Unknown'}</span>
                <span className="inline-code break-all">{ref.name}</span>
                {ref.apiVersion && (
                  <span className="text-theme-text-tertiary text-xs">{ref.apiVersion}</span>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
