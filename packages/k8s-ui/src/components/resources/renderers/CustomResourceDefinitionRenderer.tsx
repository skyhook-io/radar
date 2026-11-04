import { useMemo, useRef, useState, type ReactNode } from 'react'
import { Braces, FileCode, Tags } from 'lucide-react'
import type { ResourceRef } from '../../../types'
import { Badge } from '../../ui/Badge'
import { Collapse, CollapseChevron, useDisclosure } from '../../ui/Collapse'
import { Input } from '../../ui/Input'
import { SelectMenu } from '../../ui/SelectMenu'
import { AlertBanner, ConditionsSection, Property, PropertyList, ResourceLink, Section } from '../../ui/drawer-components'
import { getCRDStatus } from '../resource-utils-crd'
import { CRDVersionLabel } from './crd-cells'
import {
  enclosingRules, filterCRDSchema, indexCRDSchema, preferredCRDVersion,
  schemaChildren, schemaCompositions, schemaConstraints, schemaElement, schemaLeafName, schemaParentPath, schemaTypeLabel,
  type CRDSchema, type CRDVersion, type SchemaNode, type SchemaPath,
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

function SchemaFacts({ schema, elementOf, parentDescription }: { schema: CRDSchema; elementOf?: string; parentDescription?: string }) {
  const constraints = schemaConstraints(schema)
  const element = schemaElement(schema)
  const prefix = elementOf ? `${elementOf} ` : ''
  const describes = schema.description && schema.description !== parentDescription
  return <>
    {describes && <p className="whitespace-pre-wrap">{elementOf ? `Each entry (${elementOf}): ${schema.description}` : schema.description}</p>}
    {constraints.length > 0 && <div className="flex flex-wrap gap-1">{constraints.map(fact => <Badge key={fact.label} tone="structural" size="sm">{prefix}{fact.label}: {fact.value}</Badge>)}</div>}
    {schema['x-kubernetes-embedded-resource'] && <p>{elementOf ? 'Each entry is' : 'Holds'} a Kubernetes object: apiVersion, kind and metadata are implied.</p>}
    {schema['x-kubernetes-preserve-unknown-fields'] && <p>{elementOf ? 'Each entry accepts' : 'Accepts'} fields not declared in this schema.</p>}
    {schema['x-kubernetes-int-or-string'] && <p>{elementOf ? 'Each entry is' : 'Accepts'} an integer or a string.</p>}
    {schema['x-kubernetes-validations']?.map((validation, i) => <div key={i} className="card-inner">
      <div className="font-mono break-words">{validation.rule}</div>
      {validation.message && <p>{validation.message}</p>}
    </div>)}
    {schemaCompositions(schema).map(composition => <div key={composition.label}>
      <span>{prefix}{composition.label}: </span><code className="break-words whitespace-pre-wrap">{composition.value}</code>
    </div>)}
    {element && <SchemaFacts schema={element.schema} elementOf={`${prefix}${element.label}`} parentDescription={schema.description ?? parentDescription} />}
  </>
}

function ParentPath({ path }: { path: SchemaPath }) {
  const parent = schemaParentPath(path)
  if (!parent) return null
  const pieces = parent.split('.')
  return <span className="block font-mono text-xs text-theme-text-tertiary">
    {pieces.map((piece, i) => <span key={i}>{piece}{i < pieces.length - 1 && <>.<wbr /></>}</span>)}
  </span>
}

function EnclosingRulesList({ node, root }: { node: SchemaNode; root: CRDSchema }) {
  const groups = enclosingRules(node, root)
  if (groups.length === 0) return null
  return <div className="space-y-2">
    <p className="text-xs font-medium text-theme-text-tertiary">Rules on enclosing fields</p>
    {groups.map(group => <div key={group.path} className="space-y-1">
      <p className="font-mono text-xs text-theme-text-tertiary">{group.path || 'Document root'}</p>
      {group.validations.map((validation, i) => <div key={i} className="card-inner">
        <div className="font-mono break-words">{validation.rule}</div>
        {validation.message && <p>{validation.message}</p>}
      </div>)}
      {group.compositions.map(composition => <div key={composition.label}>
        <span>{composition.label}: </span><code className="break-words whitespace-pre-wrap">{composition.value}</code>
      </div>)}
    </div>)}
  </div>
}

function SchemaRows({ nodes, root }: { nodes: SchemaNode[]; root?: CRDSchema }) {
  const fullPath = !!root
  const [visible, setVisible] = useState(100)
  return <div className="space-y-0.5">
    {nodes.slice(0, fullPath ? 200 : visible).map(node => <SchemaRow key={JSON.stringify(node.path)} node={node} root={root} />)}
    {!fullPath && nodes.length > visible && <button type="button" className="text-sm text-accent" onClick={() => setVisible(n => n + 100)}>Show {Math.min(100, nodes.length - visible)} more</button>}
  </div>
}

function SchemaRow({ node, root }: { node: SchemaNode; root?: CRDSchema }) {
  const fullPath = !!root
  const [open, setOpen] = useState(false)
  const disclosure = useDisclosure(open)
  const schema = node.schema
  return <div className="border-l border-theme-border pl-2">
    <button type="button" {...disclosure.buttonProps} onClick={() => setOpen(v => !v)} className="flex w-full items-start gap-1.5 rounded py-1 text-left text-sm hover:bg-theme-hover">
      <CollapseChevron open={open} className="mt-1 h-3 w-3 shrink-0" />
      <span className="min-w-0 flex-1">
        {fullPath && <ParentPath path={node.path} />}
        <span className="flex flex-wrap items-baseline gap-x-2">
          <span className="font-mono break-all text-theme-text-primary">{schemaLeafName(node.path)}</span>
          <span className="text-xs text-theme-text-tertiary">{schemaTypeLabel(schema)}</span>
          {node.required && <Badge tone="note" size="sm">required</Badge>}
        </span>
        {!open && schema.description && <span className="block truncate text-theme-text-tertiary">{schema.description}</span>}
      </span>
    </button>
    <Collapse open={open} id={disclosure.panelId} mountLazily>
      <div className="space-y-2 pb-2 pl-5 text-sm text-theme-text-secondary">
        <SchemaFacts schema={schema} />
        {root ? <EnclosingRulesList node={node} root={root} /> : <SchemaChildren node={node} />}
      </div>
    </Collapse>
  </div>
}

function SchemaChildren({ node }: { node: SchemaNode }) {
  const children = useMemo(() => schemaChildren(node.schema, node.path, node), [node])
  return children.length > 0 ? <SchemaRows nodes={children} /> : null
}

const FIELD_CLASS = 'h-8 w-full min-w-0 rounded-md border border-theme-border bg-theme-elevated px-2.5 text-xs text-theme-text-primary placeholder:text-theme-text-tertiary focus:border-accent focus:outline-none'

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
      {schema && <Input aria-label="Filter fields" placeholder="Filter fields, e.g. spec.replicas" value={filter} onChange={e => onFilterChange(e.target.value)} className={FIELD_CLASS} />}
      {versionPicker && <div className={schema ? 'w-40 shrink-0' : 'w-40'}>{versionPicker}</div>}
    </div>
    {!schema ? <p className="text-sm text-theme-text-tertiary">Radar didn't receive a schema for this version. The YAML tab shows the object as returned.</p>
      : results ? <>
        <p className="text-sm text-theme-text-tertiary">{results.total === 1 ? '1 matching field' : `${results.total} matching fields`}{results.total > 200 ? ' — showing 200; narrow the filter' : ''}</p>
        <SchemaRows key="filter" nodes={results.nodes} root={schema} />
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
    <Section title="Definition" icon={FileCode}>
      <PropertyList>
        <Property label="Kind" value={names.kind} />
        <Property label="Group" value={spec.group} />
        <Property label="Scope" value={spec.scope} />
        <Property label="Versions" value={<div className="space-y-1">{versions.map(v => <div key={v.name}><CRDVersionLabel version={v} />{v.deprecationWarning && <p className="text-sm text-theme-text-tertiary">{v.deprecationWarning}</p>}</div>)}</div>} />
        {onViewInstances && versions.some(v => v.served) && <Property label="Instances" value={<button type="button" onClick={onViewInstances} className="text-accent hover:underline">View {names.kind} resources →</button>} />}
      </PropertyList>
    </Section>
    <Section title="Schema" icon={Braces}>
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
