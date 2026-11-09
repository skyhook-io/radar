import { useEffect, useMemo, useState } from 'react'
import { Check, Copy } from 'lucide-react'
import { Collapse, CollapseChevron, useDisclosure } from '../../ui/Collapse'
import { CodeViewer } from '../../ui/CodeViewer'
import { SelectMenu } from '../../ui/SelectMenu'
import { resourceToYaml } from '../../../utils/yaml'
import {
  displaySchemaPath, filterCRDSchema, indexCRDSchema, preferredCRDVersion,
  schemaChildren, schemaLeafName, schemaTypeLabel,
  type CRDSchema, type CRDVersion, type SchemaNode,
} from '../../../utils/crd-schema'
import { ParentRulesList, RequiredMarker, SchemaFacts, SchemaFieldFilter, SchemaPathText } from './crd-schema-parts'

export interface CRDSchemaBrowserProps {
  data: any
  theme?: 'light' | 'dark'
  onCopy?: (text: string) => void
}

function OutlineRows({ nodes, selected, onSelect, query = '' }: { nodes: SchemaNode[]; selected?: string; onSelect: (node: SchemaNode) => void; query?: string }) {
  const [visible, setVisible] = useState(100)
  return <div className="space-y-0.5">
    {nodes.slice(0, query ? 200 : visible).map(node => <OutlineRow key={displaySchemaPath(node.path)} node={node} selected={selected} onSelect={onSelect} query={query} />)}
    {!query && nodes.length > visible && <button type="button" className="py-1 text-xs text-accent" onClick={() => setVisible(n => n + 100)}>Show {Math.min(100, nodes.length - visible)} more</button>}
  </div>
}

function OutlineRow({ node, selected, onSelect, query }: { node: SchemaNode; selected?: string; onSelect: (node: SchemaNode) => void; query: string }) {
  const [open, setOpen] = useState(false)
  const disclosure = useDisclosure(open)
  const children = useMemo(() => schemaChildren(node.schema, node.path, node), [node])
  const path = displaySchemaPath(node.path)
  return <div>
    <div className={`flex items-start rounded ${selected === path ? 'selection' : 'hover:bg-theme-hover'}`}>
      {!query && children.length > 0 ? <button type="button" {...disclosure.buttonProps} aria-label={`Expand ${path}`} onClick={() => setOpen(v => !v)} className="shrink-0 p-2"><CollapseChevron open={open} className="h-3 w-3" /></button> : <span className="w-7 shrink-0" />}
      <button type="button" aria-current={selected === path ? 'true' : undefined} onClick={() => onSelect(node)} className="min-w-0 flex-1 py-1.5 pr-2 text-left text-sm">
        {query && <SchemaPathText path={node.path} query={query} part="parent" />}
        <span className="flex flex-wrap items-baseline gap-x-2">
          <span className="break-all font-mono text-theme-text-primary">{query ? <SchemaPathText path={node.path} query={query} part="leaf" /> : schemaLeafName(node.path)}</span>
          <span className="text-xs text-theme-text-tertiary">{schemaTypeLabel(node.schema)}</span>
          {node.required && <RequiredMarker />}
        </span>
      </button>
    </div>
    {!query && children.length > 0 && <Collapse open={open} id={disclosure.panelId} mountLazily><div className="border-l border-theme-border pl-3"><OutlineRows nodes={children} selected={selected} onSelect={onSelect} /></div></Collapse>}
  </div>
}

function ChildFields({ nodes, onSelect }: { nodes: SchemaNode[]; onSelect: (node: SchemaNode) => void }) {
  const [visible, setVisible] = useState(100)
  return <div className="space-y-1">
    <h3 className="text-sm font-medium text-theme-text-primary">Fields</h3>
    {nodes.slice(0, visible).map(node => <button key={displaySchemaPath(node.path)} type="button" onClick={() => onSelect(node)} className="flex w-full flex-wrap items-baseline gap-2 rounded px-2 py-1 text-left hover:bg-theme-hover">
      <span className="break-all font-mono text-sm text-accent">{schemaLeafName(node.path)}</span><span className="text-xs text-theme-text-tertiary">{schemaTypeLabel(node.schema)}</span>{node.required && <RequiredMarker />}
    </button>)}
    {nodes.length > visible && <button type="button" onClick={() => setVisible(n => n + 100)} className="text-xs text-accent">Show {Math.min(100, nodes.length - visible)} more</button>}
  </div>
}

function FieldDetail({ node, schema, onSelect, onCopy }: { node: SchemaNode; schema: CRDSchema; onSelect: (node: SchemaNode) => void; onCopy?: (text: string) => void }) {
  const path = displaySchemaPath(node.path)
  const children = useMemo(() => schemaChildren(node.schema, node.path, node), [node])
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1500)
    return () => clearTimeout(timer)
  }, [copied])
  return <div className="space-y-4 text-sm text-theme-text-secondary">
    <div className="flex items-start gap-2">
      <h2 className="min-w-0 flex-1 break-all font-mono text-theme-text-primary">{path}</h2>
      <button type="button" aria-label="Copy field path" className="flex shrink-0 items-center gap-1 text-xs hover:text-theme-text-primary" onClick={async () => {
        try {
          if (onCopy) onCopy(path)
          else await navigator.clipboard.writeText(path)
          setCopied(true)
        } catch { /* Clipboard access can be denied by the browser. */ }
      }}>{copied ? <><Check className="h-3 w-3" />Copied</> : <><Copy className="h-3 w-3" />Copy</>}</button>
    </div>
    <div className="flex items-baseline gap-2"><span className="font-mono">{schemaTypeLabel(node.schema)}</span>{node.required && <RequiredMarker />}</div>
    <SchemaFacts schema={node.schema} />
    <ParentRulesList node={node} root={schema} />
    {children.length > 0 && <ChildFields key={path} nodes={children} onSelect={onSelect} />}
  </div>
}

export function CRDSchemaBrowser(props: CRDSchemaBrowserProps) {
  return <SchemaBrowser key={JSON.stringify([props.data.metadata?.uid, props.data.metadata?.name])} {...props} />
}

function SchemaBrowser({ data, theme, onCopy }: CRDSchemaBrowserProps) {
  const versions: CRDVersion[] = data.spec?.versions ?? []
  const [picked, setPicked] = useState(() => preferredCRDVersion(versions)?.name)
  const version = versions.find(v => v.name === picked) ?? preferredCRDVersion(versions)
  const schema = version?.schema?.openAPIV3Schema
  const [filter, setFilter] = useState('')
  const [mode, setMode] = useState<'fields' | 'raw'>('fields')
  const [selection, setSelection] = useState<SchemaNode>()
  const needsIndex = Boolean(filter.trim() || selection)
  const index = useMemo(() => needsIndex ? indexCRDSchema(schema) : [], [schema, needsIndex])
  const path = selection && displaySchemaPath(selection.path)
  const selected = selection && index.find(node => displaySchemaPath(node.path) === path)
  useEffect(() => {
    if (selection && !selected) setSelection(undefined)
  }, [selection, selected])
  const nodes = useMemo(() => schema ? schemaChildren(schema) : [], [schema])
  const results = filter.trim() ? filterCRDSchema(index, filter) : undefined
  return <div className="min-w-0">
    <div className="sticky top-0 z-10 flex flex-wrap items-center gap-3 border-b border-theme-border bg-theme-surface p-3">
      {mode === 'fields' ? <SchemaFieldFilter value={filter} onChange={setFilter} /> : <p className="min-w-0 flex-1 text-xs text-theme-text-tertiary">The {version?.name} schema exactly as the CRD declares it</p>}
      {versions.length > 1 && <div className="flex items-center gap-2 text-xs text-theme-text-secondary"><span>Version</span><SelectMenu ariaLabel="Schema version" value={version?.name ?? ''} onChange={setPicked} options={versions.map(v => ({ value: v.name, label: v.name, description: [v.storage && 'storage', v.deprecated && 'deprecated', !v.served && 'not served', !v.schema?.openAPIV3Schema && 'schema not received'].filter(Boolean).join(' · ') }))} /></div>}
      <div className="flex rounded-md border border-theme-border bg-theme-base p-0.5 text-xs">
        {(['fields', 'raw'] as const).map(value => <button key={value} type="button" aria-pressed={mode === value} onClick={() => setMode(value)} className={`rounded px-2 py-1 ${mode === value ? 'bg-theme-elevated text-theme-text-primary' : 'text-theme-text-tertiary hover:text-theme-text-secondary'}`}>{value === 'fields' ? 'Fields' : 'Raw'}</button>)}
      </div>
      {mode === 'fields' && results && <p className="text-xs text-theme-text-tertiary">{results.total} matching {results.total === 1 ? 'field' : 'fields'}{results.total > 200 ? ' — showing 200; narrow the filter' : ''}</p>}
    </div>
    {!schema ? <p className="p-4 text-sm text-theme-text-tertiary">Radar didn't receive a schema for this version. The YAML tab shows the object as returned.</p>
      : mode === 'raw' ? <CodeViewer code={resourceToYaml(schema)} language="yaml" showCopyButton theme={theme} onCopy={onCopy} />
      : <div className="grid min-w-0 md:grid-cols-[2fr_3fr]">
        <div aria-label="Schema outline" className="max-h-[calc(100vh-15rem)] min-w-0 overflow-auto border-b border-theme-border p-3 md:border-b-0 md:border-r">
          <OutlineRows key={`${version?.name}:${results ? 'filter' : 'tree'}`} nodes={results?.nodes ?? nodes} selected={selected && displaySchemaPath(selected.path)} onSelect={setSelection} query={filter.trim()} />
          {!results && nodes.length === 0 && <p className="text-sm text-theme-text-tertiary">This schema declares no fields.</p>}
        </div>
        <div aria-label="Schema field detail" className="max-h-[calc(100vh-15rem)] min-w-0 overflow-auto p-4">
          {selected ? <FieldDetail key={`${version?.name}:${path}`} node={selected} schema={schema} onSelect={setSelection} onCopy={onCopy} /> : <div className="space-y-4 text-sm text-theme-text-secondary">
            <h2 className="font-medium text-theme-text-primary">{data.spec?.names?.kind} · {version?.name}</h2>
            <p>Select a field to see its rules, or paste a path from an API error into the filter.</p>
            <SchemaFacts schema={schema} />
            <ChildFields key={version?.name} nodes={nodes} onSelect={setSelection} />
          </div>}
        </div>
      </div>}
  </div>
}
