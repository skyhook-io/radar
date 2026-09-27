import { MultiSelectPicker } from '@skyhook-io/k8s-ui'
import { useState } from 'react'

const panel = { width: 280 } as const
const panelClass = 'rounded-md border border-theme-border bg-theme-surface shadow-theme-lg overflow-hidden'

const namespaces = ['argocd', 'cert-manager', 'checkout', 'default', 'ingress-nginx', 'inventory', 'kube-system', 'monitoring', 'payments']
const kinds = ['Deployment', 'Pod', 'ReplicaSet', 'Service', 'Event']

function Picker({
  items,
  initial,
  initialSearch = '',
  placeholder,
  empty,
  noItems,
  meta,
}: {
  items: string[]
  initial: string[]
  initialSearch?: string
  placeholder: string
  empty: string
  noItems: string
  meta?: (item: string) => React.ReactNode
}) {
  const [selected, setSelected] = useState<Set<string>>(new Set(initial))
  const [search, setSearch] = useState(initialSearch)
  return (
    <div style={panel} className={panelClass}>
      <MultiSelectPicker
        items={items}
        selected={selected}
        onSelectionChange={setSelected}
        onClearAll={() => setSelected(new Set())}
        onDone={() => {}}
        search={search}
        onSearchChange={setSearch}
        searchPlaceholder={placeholder}
        summaryEmptyLabel={empty}
        noItemsLabel={noItems}
        clearAllDisabled={selected.size === 0}
        renderItemMeta={meta}
      />
    </div>
  )
}

const kubeconfigTag = (ns: string) =>
  ns === 'payments' ? (
    <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary shrink-0">kubeconfig</span>
  ) : null

export function NamespaceScope() {
  return (
    <Picker
      items={namespaces}
      initial={['checkout', 'payments']}
      placeholder="Filter namespaces"
      empty="All namespaces"
      noItems="No namespaces available."
      meta={kubeconfigTag}
    />
  )
}

export function TimelineKindsNoneSelected() {
  return <Picker items={kinds} initial={[]} placeholder="Filter kinds" empty="All kinds" noItems="No kinds available." />
}

export function Searching() {
  return (
    <Picker
      items={namespaces}
      initial={[]}
      initialSearch="c"
      placeholder="Filter namespaces"
      empty="All namespaces"
      noItems="No namespaces available."
    />
  )
}

export function NoItems() {
  return <Picker items={[]} initial={[]} placeholder="Filter kinds" empty="All kinds" noItems="No kinds available." />
}
