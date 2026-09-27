import { SelectMenu } from '@skyhook-io/k8s-ui'
import { useEffect, useRef, useState } from 'react'

const namespaces = ['checkout', 'payments', 'inventory', 'monitoring', 'ingress-nginx', 'kube-system', 'cert-manager', 'argocd']
const nsOptions = [{ value: '', label: 'All scanned namespaces' }, ...namespaces.map((value) => ({ value, label: value }))]
const kindOptions = [
  { value: '', label: 'All kinds' },
  { value: 'Deployment', label: 'Deployment' },
  { value: 'StatefulSet', label: 'StatefulSet' },
  { value: 'DaemonSet', label: 'DaemonSet' },
]
const sourceOptions = [
  { value: 'auto', label: 'Automatic (recommended)' },
  { value: 'prometheus', label: 'OpenCost metrics only' },
  { value: 'kubecost', label: 'Kubecost only' },
]

export function ToolbarFilters() {
  const [ns, setNs] = useState('payments')
  const [kind, setKind] = useState('')
  return (
    <div style={{ display: 'flex', gap: 8 }}>
      <SelectMenu ariaLabel="Filter results by namespace" value={ns} onChange={setNs} className="w-52" options={nsOptions} />
      <SelectMenu ariaLabel="Filter by workload kind" value={kind} onChange={setKind} className="w-36" options={kindOptions} />
    </div>
  )
}

export function FormField() {
  const [source, setSource] = useState('auto')
  return (
    <div style={{ width: 360 }}>
      <label htmlFor="cost-source" className="mb-1 block text-sm font-medium text-theme-text-primary">Data source</label>
      <p className="mb-1 text-xs text-theme-text-tertiary">
        Automatic uses existing OpenCost metrics first, then Kubecost.
      </p>
      <SelectMenu id="cost-source" ariaLabel="Cost data source" value={source} onChange={setSource} options={sourceOptions} className="w-full" />
    </div>
  )
}

export function Disabled() {
  return (
    <div style={{ width: 360 }}>
      <label className="mb-1 block text-sm font-medium text-theme-text-primary">Data source</label>
      <SelectMenu ariaLabel="Cost data source" value="kubecost" onChange={() => {}} options={sourceOptions} className="w-full" disabled />
      <p className="mt-1 text-xs text-theme-text-tertiary">Managed by the RADAR_COST_SOURCE environment variable.</p>
    </div>
  )
}

export function OpenWithSearch() {
  const [ns, setNs] = useState('payments')
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    ref.current?.querySelector<HTMLButtonElement>('button[aria-haspopup="listbox"]')?.click()
  }, [])
  return (
    <div ref={ref} style={{ width: 240, height: 320 }}>
      <SelectMenu
        ariaLabel="Filter results by namespace"
        value={ns}
        onChange={setNs}
        options={nsOptions}
        searchPlaceholder="Search namespaces"
        className="w-full"
      />
    </div>
  )
}
