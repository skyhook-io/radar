import { useState } from 'react'
import { clsx } from 'clsx'
import { Copy } from 'lucide-react'
import { Collapse, useDisclosure } from '../../ui/Collapse'
import { Tooltip } from '../../ui/Tooltip'
import { NETWORK_POLICY_PEER_DOTS, NETWORK_POLICY_PEER_STYLES } from './network-policy-peer-styles'
import { copyText } from '../../../utils/clipboard'
import type { PolicyGroup, PolicyGroupItem } from '../../../utils/network-policy-summary'

export const COLLAPSED_ITEMS = 6
export const FILTER_THRESHOLD = 12

export function NetworkPolicyGroupList({ group }: { group: PolicyGroup }) {
  const [query, setQuery] = useState('')
  const needle = query.trim().toLowerCase()
  const shown: PolicyGroupItem[] = needle
    ? group.raw.filter((value) => value.toLowerCase().includes(needle)).map((value) => ({ label: value, members: [value] }))
    : group.items
  const mono = group.kind === 'namespaces' || group.kind === 'ipBlocks'

  return (
    <div className="min-w-0 space-y-1">
      <div className="flex items-center gap-1.5 min-w-0">
        <span className={clsx('w-1.5 h-1.5 rounded-full shrink-0', NETWORK_POLICY_PEER_DOTS[group.peerType])} />
        <span className="text-xs text-theme-text-secondary min-w-0 break-words">{group.title}</span>
        {group.raw.length > 1 && <CopyListButton values={group.raw} />}
      </div>
      {group.raw.length > FILTER_THRESHOLD && (
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={`Filter ${group.raw.length}…`}
          aria-label={`Filter ${group.title}`}
          className="w-full max-w-56 rounded border border-theme-border bg-theme-base px-2 py-0.5 text-[11px] text-theme-text-primary placeholder:text-theme-text-tertiary"
        />
      )}
      {needle && shown.length === 0 ? (
        <div className="text-[11px] text-theme-text-tertiary">No matches</div>
      ) : (
        shown.length > 0 && <ChipRows key={needle} items={shown} peerType={group.peerType} mono={mono} />
      )}
    </div>
  )
}

function ChipRows({ items, peerType, mono }: { items: PolicyGroupItem[]; peerType: PolicyGroup['peerType']; mono: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const { panelId, buttonProps } = useDisclosure(expanded)
  const head = items.slice(0, COLLAPSED_ITEMS)
  const tail = items.slice(COLLAPSED_ITEMS)
  const chip = (item: PolicyGroupItem, i: number) => <Chip key={i} item={item} className={NETWORK_POLICY_PEER_STYLES[peerType]} mono={mono} />

  return (
    <div className="min-w-0">
      <div className="flex flex-wrap gap-1">{head.map(chip)}</div>
      {tail.length > 0 && (
        <>
          {/* mountLazily: a rule can hold thousands of peers; collapsed ones are never rendered. */}
          <Collapse open={expanded} id={panelId} mountLazily>
            <div className="flex flex-wrap gap-1 mt-1">{tail.map((item, i) => chip(item, i + COLLAPSED_ITEMS))}</div>
          </Collapse>
          <button
            type="button"
            {...buttonProps}
            onClick={() => setExpanded(!expanded)}
            className="mt-1 text-[11px] text-theme-text-tertiary hover:text-theme-text-primary"
          >
            {expanded ? 'Show less' : `+${tail.length} more`}
          </button>
        </>
      )}
    </div>
  )
}

function Chip({ item, className, mono }: { item: PolicyGroupItem; className: string; mono: boolean }) {
  const range = item.members.length > 1 ? `${item.members[0]} … ${item.members[item.members.length - 1]}` : undefined
  const body = (
    <span
      data-chip
      className={clsx('inline-flex flex-wrap max-w-full items-baseline gap-x-1 rounded border px-1.5 py-0.5 text-[11px] text-theme-text-primary', className)}
    >
      <span data-chip-label className={clsx('min-w-0 break-all', mono && 'font-mono')}>{item.label}</span>
      {item.detail && <span data-chip-detail className="min-w-0 break-all text-theme-text-tertiary">{item.detail}</span>}
      {range && <span className="sr-only">{range}</span>}
    </span>
  )
  return range ? <Tooltip content={range}>{body}</Tooltip> : body
}

function CopyListButton({ values }: { values: string[] }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    if (await copyText(values.join('\n'))) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    }
  }
  return (
    <button
      type="button"
      onClick={copy}
      aria-label="Copy list"
      title={copied ? 'Copied' : `Copy ${values.length} values`}
      className="shrink-0 text-theme-text-tertiary hover:text-theme-text-primary"
    >
      <Copy className="w-3 h-3" />
    </button>
  )
}
