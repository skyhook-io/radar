import { clsx } from 'clsx'
import { Layers, GitBranch, FileText, Boxes, ScrollText } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { CodeViewer } from '../../ui/CodeViewer'
import { kindToPlural } from '../../../utils/navigation'

interface CompositionRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function extractApiGroup(apiVersion?: string): string {
  if (!apiVersion) return ''
  const slash = apiVersion.indexOf('/')
  return slash < 0 ? '' : apiVersion.slice(0, slash)
}

/** Used by both CompositionRenderer and CompositionRevisionRenderer — same shape. */
function CompositionBody({ data, onNavigate, revision }: CompositionRendererProps & { revision?: number | null }) {
  const spec = data?.spec ?? {}
  const compositeTypeRef = spec.compositeTypeRef ?? {}
  const xrdGroup = extractApiGroup(compositeTypeRef.apiVersion)
  const xrdKind = compositeTypeRef.kind
  const mode = spec.mode || 'Resources'
  const pipeline: any[] = spec.pipeline ?? []
  const resources: any[] = spec.resources ?? []
  const writeConnNs = spec.writeConnectionSecretsToNamespace
  const isPipeline = mode === 'Pipeline'
  const hasNoSteps = isPipeline && pipeline.length === 0
  const hasNoResources = !isPipeline && resources.length === 0

  return (
    <>
      {hasNoSteps && (
        <AlertBanner
          variant="warning"
          title="Empty pipeline"
          message="This Composition runs in Pipeline mode but has no functions configured. It will produce no composed resources."
        />
      )}
      {hasNoResources && (
        <AlertBanner
          variant="warning"
          title="No composed resources"
          message="This Composition has no resource templates and is not in Pipeline mode."
        />
      )}

      <Section title="Composition" icon={Layers} defaultExpanded>
        <PropertyList>
          {revision != null && <Property label="Revision" value={`#${revision}`} />}
          <Property label="Mode" value={
            <span className={clsx('badge-sm', mode === 'Pipeline' ? 'status-violet' : 'status-neutral')}>
              {mode}
            </span>
          } />
          {xrdKind && (
            <Property
              label="Composite Kind"
              value={<span className="inline-code">{xrdKind}</span>}
            />
          )}
          {compositeTypeRef.apiVersion && (
            <Property
              label="API Version"
              value={<span className="inline-code text-xs">{compositeTypeRef.apiVersion}</span>}
            />
          )}
          {writeConnNs && <Property label="Connection Secret Namespace" value={writeConnNs} />}
        </PropertyList>
        {xrdKind && (
          <div className="mt-2 text-xs text-theme-text-secondary">
            Backed by XRD:{' '}
            <ResourceLink
              name={`${kindToPlural(xrdKind)}.${xrdGroup}`}
              kind="compositeresourcedefinitions"
              namespace=""
              onNavigate={onNavigate}
              label={<span className="font-mono">{kindToPlural(xrdKind)}.{xrdGroup}</span>}
            />
          </div>
        )}
      </Section>

      {isPipeline && pipeline.length > 0 && (
        <Section title={`Pipeline (${pipeline.length} step${pipeline.length === 1 ? '' : 's'})`} icon={GitBranch} defaultExpanded>
          <div className="space-y-3">
            {pipeline.map((step: any, i: number) => {
              const fnName = step?.functionRef?.name
              const inputKind = step?.input?.kind
              const inputApiVersion = step?.input?.apiVersion
              return (
                <div key={i} className="card-inner-lg">
                  <div className="flex items-center gap-2 mb-2">
                    <span className="badge-sm status-neutral font-mono">#{i + 1}</span>
                    <span className="font-medium text-theme-text-primary">{step.step || '(unnamed step)'}</span>
                  </div>
                  <PropertyList>
                    {fnName && (
                      <Property
                        label="Function"
                        value={
                          <ResourceLink
                            name={fnName}
                            kind="functions"
                            namespace=""
                            onNavigate={onNavigate}
                          />
                        }
                      />
                    )}
                    {inputKind && (
                      <Property
                        label="Input Kind"
                        value={<span className="font-mono">{inputKind}</span>}
                      />
                    )}
                    {inputApiVersion && (
                      <Property
                        label="Input API"
                        value={<span className="font-mono text-xs text-theme-text-tertiary">{inputApiVersion}</span>}
                      />
                    )}
                  </PropertyList>
                  {step.input && (
                    <details className="mt-2">
                      <summary className="text-xs text-theme-text-tertiary cursor-pointer hover:text-theme-text-secondary">
                        Show input
                      </summary>
                      <div className="mt-1">
                        <CodeViewer code={JSON.stringify(step.input, null, 2)} language="json" maxHeight="200px" />
                      </div>
                    </details>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {!isPipeline && resources.length > 0 && (
        <Section title={`Resources (${resources.length})`} icon={Boxes} defaultExpanded>
          <div className="space-y-1">
            {resources.map((res: any, i: number) => {
              const baseKind = res?.base?.kind
              const baseApiVersion = res?.base?.apiVersion
              const patchCount = (res?.patches || []).length
              return (
                <div key={i} className="card-inner text-sm flex items-center gap-2 flex-wrap">
                  <span className="badge-sm status-neutral">{baseKind || 'Unknown'}</span>
                  <span className="inline-code break-all">{res.name || `resource-${i}`}</span>
                  {baseApiVersion && (
                    <span className="text-theme-text-tertiary text-xs">{baseApiVersion}</span>
                  )}
                  {patchCount > 0 && (
                    <span className="ml-auto text-theme-text-tertiary text-xs">
                      {patchCount} patch{patchCount === 1 ? '' : 'es'}
                    </span>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {spec.publishConnectionDetailsWithStoreConfigRef?.name && (
        <Section title="Connection Store" icon={FileText} defaultExpanded={false}>
          <PropertyList>
            <Property label="StoreConfig" value={spec.publishConnectionDetailsWithStoreConfigRef.name} />
          </PropertyList>
        </Section>
      )}
    </>
  )
}

export function CompositionRenderer({ data, onNavigate }: CompositionRendererProps) {
  return <CompositionBody data={data} onNavigate={onNavigate} />
}

interface CompositionRevisionRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CompositionRevisionRenderer({ data, onNavigate }: CompositionRevisionRendererProps) {
  const revision: number | null = data?.spec?.revision ?? null
  const sourceCompositionName = data?.metadata?.labels?.['crossplane.io/composition-name']
  // Crossplane labels active vs inactive revisions via spec.revision being the
  // highest one; the package controller doesn't write a flag. Use ownerRefs as
  // a stronger signal — the source Composition's UID controls the revision.

  return (
    <>
      {sourceCompositionName && (
        <Section title="Source Composition" icon={ScrollText} defaultExpanded>
          <PropertyList>
            <Property
              label="Composition"
              value={
                <ResourceLink
                  name={sourceCompositionName}
                  kind="compositions"
                  namespace=""
                  onNavigate={onNavigate}
                />
              }
            />
            {revision != null && <Property label="Revision" value={`#${revision}`} />}
          </PropertyList>
        </Section>
      )}
      <CompositionBody data={data} onNavigate={onNavigate} revision={revision} />
    </>
  )
}
