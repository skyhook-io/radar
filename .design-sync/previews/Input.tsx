import { Input } from '@skyhook-io/k8s-ui'

const field = 'w-full px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500 disabled:opacity-60 disabled:cursor-not-allowed'
const box = { width: 400 } as const

function Field({ id, label, help, children }: { id: string; label: string; help?: string; children: React.ReactNode }) {
  return (
    <div>
      <label htmlFor={id} className="mb-1 block text-sm font-medium text-theme-text-primary">{label}</label>
      {help && <p className="mb-1 text-xs text-theme-text-tertiary">{help}</p>}
      {children}
    </div>
  )
}

export function Placeholder() {
  return (
    <div style={box}>
      <Field id="kubecost-url" label="Kubecost URL" help="Leave blank when Kubecost runs in this cluster.">
        <Input id="kubecost-url" className={field} placeholder="Auto-discover, or https://kubecost.example.com" />
      </Field>
    </div>
  )
}

export function Filled() {
  return (
    <div style={box}>
      <Field id="cluster-id" label="Cluster ID">
        <Input id="cluster-id" className={field} defaultValue="prod-us-east-1" />
      </Field>
    </div>
  )
}

export function Disabled() {
  return (
    <div style={box}>
      <Field id="prom-url" label="Prometheus URL" help="Set by RADAR_PROMETHEUS_URL — edit the Helm values to change it.">
        <Input id="prom-url" className={field} defaultValue="http://prometheus-server.monitoring.svc:80" disabled />
      </Field>
    </div>
  )
}

export function CompactSidebarFilter() {
  return (
    <div style={{ width: 220 }}>
      <div className="text-[10px] text-theme-text-tertiary mb-1">DNS Query</div>
      <Input
        placeholder="e.g. example.com"
        className="w-full px-2 py-1 text-[11px] rounded bg-theme-elevated border border-theme-border text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
      />
    </div>
  )
}
