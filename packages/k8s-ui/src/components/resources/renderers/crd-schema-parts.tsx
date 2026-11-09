import { useState, type ReactNode } from 'react'
import { Search, X } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import { Collapse, CollapseChevron, useDisclosure } from '../../ui/Collapse'
import { Input } from '../../ui/Input'
import {
  displaySchemaPath, enclosingRules, schemaCompositions, schemaConstraints, schemaElement, schemaParentPath,
  type CRDSchema, type EnclosingRules, type SchemaNode, type SchemaPath,
} from '../../../utils/crd-schema'

export function RequiredMarker() {
  return <span className="text-xs font-semibold uppercase tracking-wide text-theme-text-secondary">required</span>
}

export function SchemaFieldFilter({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return <div className="relative min-w-0 flex-1">
    <Search aria-hidden="true" className="pointer-events-none absolute left-2.5 top-2 h-4 w-4 text-theme-text-tertiary" />
    <Input aria-label="Filter fields" placeholder="Find a field or paste an error path" value={value} onChange={e => onChange(e.target.value)} className="h-8 w-full min-w-0 rounded-md border border-theme-border bg-theme-elevated pl-8 pr-8 text-xs text-theme-text-primary placeholder:text-theme-text-tertiary focus:border-accent focus:outline-none" />
    {value && <button type="button" aria-label="Clear field filter" onClick={() => onChange('')} className="absolute right-2 top-2 text-theme-text-tertiary hover:text-theme-text-primary"><X className="h-4 w-4" /></button>}
  </div>
}

// Renders text[from, to) with the matched range marked and a break opportunity
// after each `.`, so long paths wrap between segments instead of mid-name.
function PathPiece({ text, from, to, match }: { text: string; from: number; to: number; match: [number, number] | null }) {
  const parts: ReactNode[] = []
  const pushPlain = (start: number, end: number) => {
    text.slice(start, end).split(/(?<=\.)/).forEach((piece, i, all) => {
      parts.push(<span key={`${start}-${i}`}>{piece}</span>)
      if (piece.endsWith('.') && i < all.length - 1) parts.push(<wbr key={`${start}-${i}-w`} />)
    })
  }
  const [ms, me] = match ? [Math.max(match[0], from), Math.min(match[1], to)] : [0, 0]
  if (!match || ms >= me) { pushPlain(from, to); return <>{parts}</> }
  pushPlain(from, ms)
  parts.push(<mark key="m" className="rounded-sm bg-accent-muted text-theme-text-primary">{text.slice(ms, me)}</mark>)
  pushPlain(me, to)
  return <>{parts}</>
}

/** One part of a search hit's path, with the query's match marked across the
 *  parent/field boundary: the muted parent path, or the field name itself. */
export function SchemaPathText({ path, query = '', part }: { path: SchemaPath; query?: string; part: 'parent' | 'leaf' }) {
  const text = displaySchemaPath(path)
  const parentLength = schemaParentPath(path).length
  const term = query.trim().toLowerCase()
  const at = term ? text.toLowerCase().indexOf(term) : -1
  const match: [number, number] | null = at < 0 ? null : [at, at + term.length]
  if (part === 'parent') {
    return parentLength > 0 ? <span className="block font-mono text-xs text-theme-text-tertiary [overflow-wrap:anywhere]"><PathPiece text={text} from={0} to={parentLength} match={match} /></span> : null
  }
  return <PathPiece text={text} from={parentLength} to={text.length} match={match} />
}

export function SchemaRules({ rules }: { rules: NonNullable<CRDSchema['x-kubernetes-validations']> }) {
  return <>{rules.map((validation, i) => <div key={i} className="card-inner space-y-1">
    {validation.message && <p className="text-sm text-theme-text-secondary">{validation.message}</p>}
    <div className={`whitespace-pre-wrap break-words font-mono text-xs ${validation.message ? 'text-theme-text-tertiary' : 'text-theme-text-secondary'}`}>{validation.rule}</div>
  </div>)}</>
}

function ConstraintDisplay({ label, value, block }: { label: string; value: string; block: boolean }) {
  return block ? <div className="min-w-0 w-full space-y-1">
    <p className="text-xs text-theme-text-secondary">{label}</p>
    <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded bg-theme-elevated p-3 font-mono text-xs text-theme-text-secondary">{value}</pre>
  </div> : <Badge tone="structural" size="sm">{label}: {value}</Badge>
}

export function SchemaFacts({ schema, elementOf, parentDescription, showDescription = true }: { schema: CRDSchema; elementOf?: string; parentDescription?: string; showDescription?: boolean }) {
  const element = schemaElement(schema)
  const prefix = elementOf ? `${elementOf} ` : ''
  return <>
    {showDescription && schema.description && schema.description !== parentDescription && <p className="whitespace-pre-wrap">{elementOf ? `Each entry (${elementOf}): ${schema.description}` : schema.description}</p>}
    <div className="flex flex-wrap gap-1">{schemaConstraints(schema).map(fact => <ConstraintDisplay key={fact.label} label={`${prefix}${fact.label}`} value={fact.value} block={fact.value.length > 40 || (fact.label === 'default' && schema.default !== null && typeof schema.default === 'object')} />)}</div>
    {schema['x-kubernetes-embedded-resource'] && <p>{elementOf ? 'Each entry is' : 'Holds'} a Kubernetes object: apiVersion, kind and metadata are implied.</p>}
    {schema['x-kubernetes-preserve-unknown-fields'] && <p>{elementOf ? 'Each entry accepts' : 'Accepts'} fields not declared in this schema.</p>}
    {schema['x-kubernetes-int-or-string'] && <p>{elementOf ? 'Each entry is' : 'Accepts'} an integer or a string.</p>}
    <SchemaRules rules={schema['x-kubernetes-validations'] ?? []} />
    {schemaCompositions(schema).map(composition => <div key={composition.label}>
      <span>{prefix}{composition.label}: </span><code className="break-words whitespace-pre-wrap">{composition.value}</code>
    </div>)}
    {element && <SchemaFacts schema={element.schema} elementOf={`${prefix}${element.label}`} parentDescription={schema.description ?? parentDescription} />}
  </>
}

function ParentRuleGroup({ group, defaultOpen }: { group: EnclosingRules; defaultOpen: boolean }) {
  const [groupOpen, setGroupOpen] = useState(defaultOpen)
  const [moreOpen, setMoreOpen] = useState(false)
  const groupDisclosure = useDisclosure(groupOpen)
  const moreDisclosure = useDisclosure(moreOpen)
  const count = group.validations.length + group.compositions.length
  const rules = [
    ...group.validations.map((rule, i) => <SchemaRules key={`v${i}`} rules={[rule]} />),
    ...group.compositions.map(composition => <div key={composition.label}><span>{composition.label}: </span><code className="whitespace-pre-wrap break-words">{composition.value}</code></div>),
  ]
  return <div className="space-y-1">
    <button type="button" {...groupDisclosure.buttonProps} onClick={() => setGroupOpen(v => !v)} className="flex items-start gap-1 text-left text-xs text-theme-text-tertiary hover:text-theme-text-secondary">
      <CollapseChevron open={groupOpen} className="mt-0.5 h-3 w-3 shrink-0" />
      <span className="break-words">{count} {count === 1 ? 'rule' : 'rules'} on <span className="font-mono">{group.path || 'Document root'}</span></span>
    </button>
    <Collapse open={groupOpen} id={groupDisclosure.panelId} mountLazily>
      <div className="space-y-1">
        {rules.slice(0, 3)}
        {count > 3 && <>
          <button type="button" {...moreDisclosure.buttonProps} onClick={() => setMoreOpen(v => !v)} className="text-xs text-accent">{moreOpen ? 'Show fewer' : `Show ${count - 3} more`}</button>
          <Collapse open={moreOpen} id={moreDisclosure.panelId} mountLazily><div className="space-y-1">{rules.slice(3)}</div></Collapse>
        </>}
      </div>
    </Collapse>
  </div>
}

// The nearest parent's rules are the likeliest to concern the field; farther
// groups start collapsed to their path and count.
export function ParentRulesList({ node, root }: { node: SchemaNode; root: CRDSchema }) {
  const groups = enclosingRules(node, root)
  return groups.length ? <div className="space-y-2">
    <p className="text-xs font-medium text-theme-text-tertiary">Rules on parent fields</p>
    {groups.map((group, i) => <ParentRuleGroup key={group.path} group={group} defaultOpen={i === 0} />)}
  </div> : null
}
