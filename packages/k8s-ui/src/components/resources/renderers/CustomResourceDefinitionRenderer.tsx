import { useMemo, useRef, useState, type ReactNode } from 'react'
import { Braces, FileCode, Tags } from 'lucide-react'
import type { ResourceRef } from '../../../types'
import { Collapse, CollapseChevron, useDisclosure } from '../../ui/Collapse'
import { SelectMenu } from '../../ui/SelectMenu'
import { AlertBanner, ConditionsSection, Property, PropertyList, ResourceLink, Section } from '../../ui/drawer-components'
import { getCRDStatus } from '../resource-utils-crd'
import { CRDVersionLabel } from './crd-cells'
import { ParentRulesList, RequiredMarker, SchemaFacts, SchemaFieldFilter, SchemaPathText } from './crd-schema-parts'
import {
  filterCRDSchema, indexCRDSchema, preferredCRDVersion,
  schemaChildren, schemaLeafName, schemaTypeLabel,
  type CRDSchema, type CRDVersion, type SchemaNode,
} from '../../../utils/crd-schema'

export interface CustomResourceDefinitionRendererProps {
  data: any
  onNavigate?: (ref: ResourceRef) => void
  onViewInstances?: () => void
}

function versionNotes(version: CRDVersion): string | undefined {
  const notes = [
    version.storage && 'storage',
    version.deprecated && 'deprecated',
    !version.served && 'not served',
    !version.schema?.openAPIV3Schema && 'schema not received',
  ].filter(Boolean)
  return notes.length ? notes.join(' · ') : undefined
}

function SchemaRows({ nodes, root, query }: { nodes: SchemaNode[]; root?: CRDSchema; query?: string }) {
  const fullPath = !!root
  const [visible, setVisible] = useState(100)
  return <div className="space-y-0.5">
    {nodes.slice(0, fullPath ? 200 : visible).map(node => <SchemaRow key={JSON.stringify(node.path)} node={node} root={root} query={query} />)}
    {!fullPath && nodes.length > visible && <button type="button" className="text-sm text-accent" onClick={() => setVisible(n => n + 100)}>Show {Math.min(100, nodes.length - visible)} more</button>}
  </div>
}

function SchemaRow({ node, root, query }: { node: SchemaNode; root?: CRDSchema; query?: string }) {
  const fullPath = !!root
  const [open, setOpen] = useState(false)
  const disclosure = useDisclosure(open)
  const schema = node.schema
  return <div className="border-l border-theme-border pl-2">
    <button type="button" {...disclosure.buttonProps} onClick={() => setOpen(v => !v)} className="flex w-full items-start gap-1.5 rounded py-1 text-left text-sm hover:bg-theme-hover">
      <CollapseChevron open={open} className="mt-1 h-3 w-3 shrink-0" />
      <span className="min-w-0 flex-1">
        {fullPath && <SchemaPathText path={node.path} query={query} part="parent" />}
        <span className="flex flex-wrap items-baseline gap-x-2">
          <span className="font-mono break-all text-theme-text-primary">{fullPath ? <SchemaPathText path={node.path} query={query} part="leaf" /> : schemaLeafName(node.path)}</span>
          <span className="text-xs text-theme-text-tertiary">{schemaTypeLabel(schema)}</span>
          {node.required && <RequiredMarker />}
        </span>
        {schema.description && !(node.path.length === 1 && ['apiVersion', 'kind', 'metadata'].includes(schemaLeafName(node.path))) && <span className="block truncate text-theme-text-tertiary">{schema.description}</span>}
      </span>
    </button>
    <Collapse open={open} id={disclosure.panelId} mountLazily>
      <div className="space-y-2 pb-2 pl-5 text-sm text-theme-text-secondary">
        <SchemaFacts schema={schema} showDescription={(schema.description?.length ?? 0) > 100} />
        {root ? <ParentRulesList node={node} root={root} /> : <SchemaChildren node={node} />}
      </div>
    </Collapse>
  </div>
}

function SchemaChildren({ node }: { node: SchemaNode }) {
  const children = useMemo(() => schemaChildren(node.schema, node.path, node), [node])
  return children.length > 0 ? <SchemaRows nodes={children} /> : null
}

function SchemaExplorer({ version, versionPicker, filter, onFilterChange }: {
  version?: CRDVersion
  versionPicker?: ReactNode
  filter: string
  onFilterChange: (filter: string) => void
}) {
  const index = useRef<{ schema: CRDSchema; nodes: SchemaNode[] } | null>(null)
  const schema = version?.schema?.openAPIV3Schema
  const nodes = useMemo(() => schema ? schemaChildren(schema) : [], [schema])
  let results: ReturnType<typeof filterCRDSchema> | undefined
  if (schema && filter.trim()) {
    if (index.current?.schema !== schema) index.current = { schema, nodes: indexCRDSchema(schema) }
    results = filterCRDSchema(index.current.nodes, filter)
  }
  return <div className="space-y-3">
    <div className="flex items-center gap-2">
      {schema && <SchemaFieldFilter value={filter} onChange={onFilterChange} />}
      {versionPicker && <div className="flex shrink-0 items-center gap-2 text-xs text-theme-text-secondary"><span>Version</span><div className="w-32">{versionPicker}</div></div>}
    </div>
    {!schema ? <p className="text-sm text-theme-text-tertiary">Radar didn't receive a schema for this version. The YAML tab shows the object as returned.</p>
      : results ? <>
        <p className="text-sm text-theme-text-tertiary">{results.total === 1 ? '1 matching field' : `${results.total} matching fields`}{results.total > 200 ? ' — showing 200; narrow the filter' : ''}</p>
        <SchemaRows key="filter" nodes={results.nodes} root={schema} query={filter} />
      </> : <>
        <div className="space-y-2 text-sm text-theme-text-secondary"><SchemaFacts schema={{ ...schema, description: undefined }} /></div>
        {nodes.length > 0 ? <SchemaRows key="tree" nodes={nodes} /> : <p className="text-sm text-theme-text-tertiary">This schema declares no fields.</p>}
      </>}
  </div>
}

export function CustomResourceDefinitionRenderer(props: CustomResourceDefinitionRendererProps) {
  const identity = JSON.stringify([props.data.metadata?.uid, props.data.metadata?.name])
  return <CRDDefinition key={identity} {...props} identity={identity} />
}

function CRDDefinition({ data, onNavigate, onViewInstances, identity }: CustomResourceDefinitionRendererProps & { identity: string }) {
  const spec = data.spec || {}
  const names = spec.names || {}
  const versions: CRDVersion[] = spec.versions || []
  const [picked, setSelected] = useState(() => preferredCRDVersion(versions)?.name)
  const [filter, setFilter] = useState('')
  // An update can remove the version being read; fall back rather than
  // reporting its schema as missing.
  const version = versions.find(v => v.name === picked) ?? preferredCRDVersion(versions)
  const selected = version?.name
  const status = getCRDStatus(data)
  const service = spec.conversion?.webhook?.clientConfig?.service
  const allMissing = versions.every(v => !v.schema?.openAPIV3Schema)
  return <>
    {(status.tone === 'unhealthy' || status.tone === 'degraded') && <AlertBanner variant={status.tone === 'unhealthy' ? 'error' : 'warning'} title={status.text} message={status.reason} />}
    <Section title="Definition" icon={FileCode} defaultExpanded={true}>
      <PropertyList>
        <Property label="Kind" value={names.kind} />
        <Property label="Group" value={spec.group} />
        <Property label="Scope" value={spec.scope} />
        <Property label="Versions" value={<div className="space-y-1">{versions.map(v => <div key={v.name}><CRDVersionLabel version={v} />{v.deprecationWarning && <p className="text-sm text-theme-text-tertiary">{v.deprecationWarning}</p>}</div>)}</div>} />
        {onViewInstances && versions.some(v => v.served) && <Property label="Instances" value={<button type="button" onClick={onViewInstances} className="text-accent hover:underline">View {names.kind} resources →</button>} />}
      </PropertyList>
    </Section>
    <Section title="Schema" icon={Braces} defaultExpanded={true}>
      <div className="space-y-3">
        {allMissing ? <p className="text-sm text-theme-text-tertiary">Radar didn't receive this definition's schema. The YAML tab shows the object as returned.</p>
          : <SchemaExplorer key={`${identity}:${selected}`} version={version} filter={filter} onFilterChange={setFilter} versionPicker={versions.length > 1 && <SelectMenu ariaLabel="Schema version" value={selected || ''} onChange={setSelected}
            options={versions.map(v => ({ value: v.name, label: v.name, description: versionNotes(v) }))} />} />}
      </div>
    </Section>
    <Section title="Names & conversion" icon={Tags} defaultExpanded={false}>
      <PropertyList>
        <Property label="Plural" value={names.plural} />
        <Property label="Singular" value={names.singular} />
        <Property label="List kind" value={names.listKind} />
        <Property label="Short names" value={names.shortNames?.join(', ')} />
        <Property label="Categories" value={names.categories?.join(', ')} />
        <Property label="Conversion strategy" value={spec.conversion?.strategy} />
        {service && <Property label="Webhook service" value={<><ResourceLink kind="Service" namespace={service.namespace} name={service.name} onNavigate={onNavigate} />{service.path && <span className="font-mono ml-1">{service.path}</span>}</>} />}
      </PropertyList>
    </Section>
    <ConditionsSection conditions={data.status?.conditions} />
  </>
}
