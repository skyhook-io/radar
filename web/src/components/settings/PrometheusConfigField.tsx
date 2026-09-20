import { useEffect, useRef, useState, type ComponentProps } from 'react'
import { Badge } from '@skyhook-io/k8s-ui'
import { apiUrl, getApiBase, getAuthHeaders, getCredentialsMode } from '../../api/config'
import { Disclosure } from '@skyhook-io/k8s-ui'
import { PrometheusConnectionForm, type PrometheusApplyResult } from './PrometheusConnectionForm'

export interface PrometheusProfileView {
  target: { binding: string; context: string; source?: string; fingerprint: string; clientGeneration: number; operationGeneration: number }
  revision: string
  state: 'auto' | 'saved' | 'launch' | 'target_changed' | 'error'
  url: string
  headerKeys: string[]
  headersManaged: boolean
  error?: string
  legacy?: { url: string; headerKeys: string[]; revision: string; error?: string }
}

type Props = Omit<ComponentProps<typeof PrometheusConnectionForm>, 'onApply'> & {
  profile?: PrometheusProfileView
  onProfileChange: (profile: PrometheusProfileView) => void
  onReload: () => void
}

export function PrometheusConfigField({ profile, onProfileChange, onReload, onApplied, ...form }: Props) {
  const request = useRef<AbortController | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [replacing, setReplacing] = useState(false)
  useEffect(() => () => request.current?.abort(), [])

  const apply = async (url: string, headers?: Record<string, string>, action = 'apply'): Promise<PrometheusApplyResult> => {
    if (request.current) throw new Error('A connection update is already in progress.')
    const controller = new AbortController()
    request.current = controller
    const base = getApiBase()
    setBusy(true)
    try {
      const response = await fetch(apiUrl('/integrations/prometheus'), {
        method: 'PUT', signal: controller.signal,
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify({ prometheusUrl: url, ...(headers !== undefined ? { headers } : {}),
          ...(profile ? { target: profile.target, revision: profile.revision, action, legacyRevision: profile.legacy?.revision } : {}),
        }),
      })
      const data = await response.json() as PrometheusApplyResult & { profile: PrometheusProfileView }
      if (controller.signal.aborted || base !== getApiBase()) throw new Error('Cluster changed; reload Settings.')
      if (!response.ok) throw new Error(data.error || response.statusText)
      if (profile) {
        onProfileChange(data.profile)
        onApplied?.(data.profile.url)
        setReplacing(false)
      }
      return data
    } finally {
      if (request.current === controller) request.current = null
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  const act = async (action: string) => {
    setError(null)
    try {
      const result = await apply(form.value, undefined, action)
      if (result.error && action !== 'finish_migration') setError(result.error)
    } catch (err) {
      if (!request.current?.signal.aborted) setError(err instanceof Error ? err.message : String(err))
    }
  }

  if (!profile) return <fieldset disabled={busy} className="min-w-0"><PrometheusConnectionForm {...form} onApplied={onApplied} onApply={apply} /></fieldset>
  const blocked = profile.state === 'target_changed' || profile.state === 'error'
  return (
    <fieldset disabled={busy} className="min-w-0 space-y-3">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-theme-text-primary min-w-0 break-words">Cluster: <span className="font-medium">{profile.target.context || 'Not selected'}</span></p>
        <button className="text-xs text-accent-text hover:underline shrink-0" onClick={onReload} disabled={busy}>Reload settings</button>
      </div>
      {profile.state === 'launch' ? (
        <div className="card-inner-lg space-y-2">
          <Badge tone="note">Set for this launch</Badge>
          <p className="text-sm text-theme-text-primary break-all">{profile.url || 'Auto-discovery'}</p>
          <p className="text-xs text-theme-text-secondary">{profile.headerKeys.length ? `Headers: ${profile.headerKeys.join(', ')} (values hidden)` : 'No auth headers'}</p>
          <p className="text-xs text-theme-text-secondary">This override stays with this cluster. Restart without Prometheus flags to edit its saved connection.</p>
        </div>
      ) : replacing ? (
        <div className="space-y-3">
          <p className="text-sm text-theme-text-secondary">Save a new connection for this cluster. Previous headers will not be reused.</p>
          <PrometheusConnectionForm {...form} configuredHeaderKeys={[]} headersManaged={false} serverManaged={false} urlFromFlag={false} onApply={(url, headers) => apply(url, headers ?? {}, 'replace')} />
          <button className="text-xs text-theme-text-secondary hover:underline" disabled={busy} onClick={() => { setReplacing(false); form.onChange(profile.url) }}>Cancel replacement</button>
        </div>
      ) : blocked ? (
        <div className="card-inner-lg space-y-2">
          <Badge severity="warning">{profile.state === 'target_changed' ? 'Cluster connection changed' : 'Settings unavailable'}</Badge>
          <p className="text-sm text-theme-text-secondary">{profile.state === 'target_changed' ? 'This kubeconfig context now has a different server, trust configuration or user reference. Its saved metrics connection is paused.' : profile.error}</p>
          {profile.state === 'target_changed' && <>
            <p className="text-sm text-theme-text-primary break-all">{profile.url || 'Auto-discovery'}</p>
            <button className="btn-brand px-3 py-1.5 text-xs" disabled={busy} onClick={() => void act('reconfirm')}>Use this connection for {profile.target.context}</button>
            <button className="block text-xs text-accent-text hover:underline" disabled={busy} onClick={() => { form.onChange(''); setReplacing(true) }}>Use a different connection</button>
          </>}
        </div>
      ) : <>
        {profile.legacy && (
          <div className="card-inner-lg space-y-2">
            <p className="text-sm font-medium text-theme-text-primary">Previously saved connection</p>
            <p className="text-xs text-theme-text-secondary">These older settings were not linked to a cluster. Use them here only if this backend serves {profile.target.context}.</p>
            <p className="text-sm text-theme-text-primary break-all">{profile.legacy.url || 'No URL saved'}</p>
            {profile.legacy.headerKeys.length > 0 && <p className="text-xs text-theme-text-secondary">Headers: {profile.legacy.headerKeys.join(', ')} (values hidden)</p>}
            {profile.legacy.error && <p className="text-xs text-warning-text">{profile.legacy.error}. Correct the older connection in config.json and reload Settings, or configure a new connection below.</p>}
            <div className="flex flex-wrap items-center gap-3">
              <button className="btn-brand px-3 py-1.5 text-xs disabled:opacity-50" disabled={busy || !!profile.legacy.error} onClick={() => void act('adopt')}>Save for this cluster</button>
              <button className="text-xs text-theme-text-secondary hover:underline" disabled={busy} onClick={() => void act('finish_migration')}>Stop offering legacy settings for all clusters</button>
            </div>
          </div>
        )}
        <PrometheusConnectionForm {...form} configuredHeaderKeys={profile.headerKeys} headersManaged={profile.headersManaged} serverManaged={false} urlFromFlag={false} scopeDescription={`Saved for ${profile.target.context}. Other contexts keep their own connections.`} onApply={apply} />
      </>}
      {error && <p role="alert" className="text-sm text-warning-text">{error}</p>}
      <Disclosure summary="Storage and cluster identity" className="text-xs text-theme-text-tertiary">
        <div className="space-y-2 pt-2">
          <p>Stored in ~/.radar/clusters.json, shared by CLI and Desktop. Credentials are permission-protected plaintext, not encrypted. Separate kubeconfig contexts have separate profiles.</p>
          {profile.target.source && <p className="break-all">Source: {profile.target.source}</p>}
          {profile.headersManaged && <p>Header values come from environment references. Edit headersFromEnv in clusters.json to change those references.</p>}
        </div>
      </Disclosure>
    </fieldset>
  )
}
