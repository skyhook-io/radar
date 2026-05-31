import { clsx } from 'clsx'
import { FileCode2, Layers, KeyRound, Settings } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'

interface XRDRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function XRDRenderer({ data, onNavigate }: XRDRendererProps) {
  const spec = data?.spec ?? {}
  const conditions: any[] = data?.status?.conditions ?? []
  const established = conditions.find((c: any) => c.type === 'Established')
  const offered = conditions.find((c: any) => c.type === 'Offered')
  const group = spec.group ?? ''
  const names = spec.names ?? {}
  const claimNames = spec.claimNames
  const versions: any[] = spec.versions ?? []
  const scope = spec.scope // v2 only
  const connectionSecretKeys: string[] = spec.connectionSecretKeys ?? []
  const defaultCompositionRef = spec.defaultCompositionRef
  const enforcedCompositionRef = spec.enforcedCompositionRef
  const defaultCompositeDeletePolicy = spec.defaultCompositeDeletePolicy

  return (
    <>
      {established?.status === 'False' && (
        <AlertBanner
          variant="error"
          title="XRD Not Established"
          message={established.message || established.reason || 'The XRD has not produced a CRD. Check schema validity.'}
        />
      )}
      {offered?.status === 'False' && claimNames && (
        <AlertBanner
          variant="warning"
          title="Claim Not Offered"
          message={offered.message || offered.reason || 'Claim CRD has not been produced from the claimNames spec.'}
        />
      )}

      <Section title="Generated CR" icon={FileCode2} defaultExpanded>
        <PropertyList>
          {names.kind && (
            <Property label="Kind" value={<span className="font-mono">{names.kind}</span>} />
          )}
          {names.plural && (
            <Property label="Plural" value={<span className="font-mono">{names.plural}</span>} />
          )}
          {names.singular && (
            <Property label="Singular" value={<span className="font-mono">{names.singular}</span>} />
          )}
          {names.listKind && (
            <Property label="List Kind" value={<span className="inline-code">{names.listKind}</span>} />
          )}
          <Property label="Group" value={<span className="inline-code break-all">{group}</span>} />
          {scope && (
            <Property
              label="Scope"
              value={
                <span className={clsx('badge-sm', scope === 'Cluster' ? 'status-violet' : 'status-neutral')}>
                  {scope}
                </span>
              }
            />
          )}
        </PropertyList>
      </Section>

      {claimNames && (
        <Section title="Claim (v1)" icon={Layers} defaultExpanded>
          <PropertyList>
            {claimNames.kind && (
              <Property label="Kind" value={<span className="font-mono">{claimNames.kind}</span>} />
            )}
            {claimNames.plural && (
              <Property label="Plural" value={<span className="font-mono">{claimNames.plural}</span>} />
            )}
            {claimNames.singular && (
              <Property label="Singular" value={<span className="font-mono">{claimNames.singular}</span>} />
            )}
          </PropertyList>
        </Section>
      )}

      {versions.length > 0 && (
        <Section title={`Versions (${versions.length})`} icon={FileCode2} defaultExpanded>
          <div className="space-y-1">
            {versions.map((v: any, i: number) => (
              <div key={i} className="card-inner text-sm flex items-center gap-2 flex-wrap">
                <span className="font-mono text-theme-text-primary">{v.name}</span>
                {v.served && <span className="badge-sm status-healthy">served</span>}
                {v.referenceable && <span className="badge-sm status-neutral">referenceable</span>}
                {v.deprecated && <span className="badge-sm status-alert">deprecated</span>}
                {v.deprecationWarning && (
                  <span className="text-theme-text-tertiary text-xs break-all">{v.deprecationWarning}</span>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {(defaultCompositionRef?.name || enforcedCompositionRef?.name) && (
        <Section title="Composition" icon={Settings} defaultExpanded>
          <PropertyList>
            {defaultCompositionRef?.name && (
              <Property
                label="Default"
                value={
                  <ResourceLink
                    name={defaultCompositionRef.name}
                    kind="compositions"
                    namespace=""
                    onNavigate={onNavigate}
                  />
                }
              />
            )}
            {enforcedCompositionRef?.name && (
              <Property
                label="Enforced"
                value={
                  <ResourceLink
                    name={enforcedCompositionRef.name}
                    kind="compositions"
                    namespace=""
                    onNavigate={onNavigate}
                  />
                }
              />
            )}
            {defaultCompositeDeletePolicy && (
              <Property label="Default Delete Policy" value={defaultCompositeDeletePolicy} />
            )}
          </PropertyList>
        </Section>
      )}

      {connectionSecretKeys.length > 0 && (
        <Section title={`Connection Secret Keys (${connectionSecretKeys.length})`} icon={KeyRound} defaultExpanded={false}>
          <div className="flex flex-wrap gap-1">
            {connectionSecretKeys.map((k: string) => (
              <span key={k} className="badge-sm status-neutral font-mono">{k}</span>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
