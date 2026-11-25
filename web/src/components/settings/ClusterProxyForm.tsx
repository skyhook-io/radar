import { useId, useState } from 'react'
import { ConfirmDialog, Input, pluralize } from '@skyhook-io/k8s-ui'
import { Collapse } from '@skyhook-io/k8s-ui/components/ui/Collapse'
import { fetchSessionCounts, useContexts, useSetContextProxy } from '../../api/client'
import { useConnection } from '../../context/ConnectionContext'
import { useDock } from '../dock'
import { Tooltip } from '../ui/Tooltip'

/**
 * Edits the proxy Radar uses to reach one context's API server, without
 * touching the kubeconfig. The server saves a proxy only after the cluster
 * answered through it; `onSaved` then reconnects.
 */
export function ClusterProxyForm({ context, savedProxyUrl, onSaved, confirmBeforeSave }: {
  context: string
  /** The saved proxy without credentials, as the context list reports it. */
  savedProxyUrl?: string
  onSaved?: (result: { current: boolean }) => void
  /** Resolves false to cancel the save, e.g. when reconnecting would end open sessions. */
  confirmBeforeSave?: () => Promise<boolean>
}) {
  const inputId = useId()
  const [value, setValue] = useState('')
  const save = useSetContextProxy()
  const submit = async (proxyUrl: string) => {
    if (confirmBeforeSave && !(await confirmBeforeSave())) return
    save.mutate({ name: context, proxyUrl }, {
      onSuccess: (result) => {
        setValue('')
        onSaved?.(result)
      },
    })
  }

  return (
    <div className="space-y-2 text-left">
      <label htmlFor={inputId} className="block text-xs text-theme-text-secondary">
        HTTP, HTTPS or SOCKS5 proxy for <span className="inline-code">{context}</span>. Used instead of the kubeconfig's proxy-url; your kubeconfig isn't changed. Radar checks the proxy before saving it, then reconnects.
      </label>
      {savedProxyUrl && (
        <p className="text-xs text-theme-text-secondary">
          Saved: <span className="inline-code">{savedProxyUrl}</span>
        </p>
      )}
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (value.trim()) submit(value.trim())
        }}
      >
        <Input
          id={inputId}
          value={value}
          disabled={save.isPending}
          onChange={(e) => setValue(e.target.value)}
          placeholder="socks5://127.0.0.1:1080 or http://proxy:3128"
          autoComplete="off"
          spellCheck={false}
          className="flex-1 min-w-0 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
        />
        <button type="submit" disabled={save.isPending || !value.trim()} className="btn-brand px-3 py-1.5 text-xs rounded-md">
          {save.isPending ? 'Checking…' : savedProxyUrl ? 'Replace' : 'Save'}
        </button>
        {savedProxyUrl && (
          <button
            type="button"
            disabled={save.isPending}
            onClick={() => submit('')}
            className="px-3 py-1.5 text-xs rounded-md border border-theme-border bg-theme-surface text-theme-text-primary hover:bg-theme-hover disabled:opacity-50"
          >
            Remove
          </button>
        )}
      </form>
    </div>
  )
}

/**
 * Every kubeconfig context with its saved proxy, so a cluster can get one
 * before Radar ever connects to it. Saving for the active context reconnects,
 * which ends port forwards and terminals like a context switch does, so it
 * asks first; other contexts use the proxy on the next switch.
 */
export function ClusterProxyList() {
  const { data: contexts } = useContexts()
  const { retry } = useConnection()
  const { tabs } = useDock()
  const [editing, setEditing] = useState<string | null>(null)
  const [pending, setPending] = useState<{ sessions: string; resolve: (ok: boolean) => void } | null>(null)
  if (!contexts?.length) return null
  const sorted = [...contexts].sort((a, b) => Number(b.isCurrent) - Number(a.isCurrent))

  const confirmBeforeSave = async () => {
    // Best effort, like the context switch: an unknown count doesn't block saving.
    const portForwards = await fetchSessionCounts().then((c) => c.portForwards, () => 0)
    const terminals = tabs.filter((t) => t.type === 'terminal').length
    if (portForwards + terminals === 0) return true
    const sessions = [
      portForwards > 0 && pluralize(portForwards, 'port forward'),
      terminals > 0 && pluralize(terminals, 'terminal session'),
    ].filter(Boolean).join(' and ')
    return new Promise<boolean>((resolve) => setPending({ sessions, resolve }))
  }
  const close = (ok: boolean) => {
    pending?.resolve(ok)
    setPending(null)
  }

  return (
    <>
      <ul className="max-h-96 overflow-y-auto rounded-md border border-theme-border divide-y divide-theme-border">
        {sorted.map((c) => (
          <li key={c.name} className="px-3 py-2">
            <div className="flex items-center gap-2 text-sm">
              <Tooltip content={c.name} wrapperClassName="min-w-0"><span className="block truncate text-theme-text-primary">{c.name}</span></Tooltip>
              {c.isCurrent && <span className="shrink-0 text-xs text-theme-text-tertiary">current</span>}
              <span className="ml-auto min-w-0 truncate text-xs text-theme-text-secondary">
                {c.proxyUrl ? <span className="inline-code">{c.proxyUrl}</span> : 'No proxy saved'}
              </span>
              <button
                type="button"
                aria-expanded={editing === c.name}
                onClick={() => setEditing(editing === c.name ? null : c.name)}
                className="shrink-0 text-xs text-accent-text hover:underline"
              >
                {editing === c.name ? 'Close' : c.proxyUrl ? 'Change' : 'Set proxy'}
              </button>
            </div>
            <Collapse open={editing === c.name}>
              <div className="pt-2">
                <ClusterProxyForm
                  context={c.name}
                  savedProxyUrl={c.proxyUrl}
                  confirmBeforeSave={c.isCurrent ? confirmBeforeSave : undefined}
                  onSaved={({ current }) => {
                    setEditing(null)
                    if (current) retry()
                  }}
                />
              </div>
            </Collapse>
          </li>
        ))}
      </ul>
      <ConfirmDialog
        open={pending != null}
        onClose={() => close(false)}
        onConfirm={() => close(true)}
        title="Reconnect to use the proxy?"
        message={`Radar reconnects to this cluster to use the new proxy, which ends ${pending?.sessions ?? 'open sessions'}.`}
        confirmLabel="Save and reconnect"
        variant="warning"
      />
    </>
  )
}
