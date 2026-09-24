import { useEffect, useRef, useState } from 'react'
import { Badge, ConfirmDialog, Disclosure } from '@skyhook-io/k8s-ui'
import { trapConnectionConfirmationFocus } from './connection-confirmation'
import {
  apiUrl,
  getApiBase,
  getAuthHeaders,
  getCredentialsMode,
} from '../../api/config'
import {
  integrationNames,
  type ConnectionResponse,
  type IntegrationProfiles,
  type StoredConnection,
} from './LocalConnectionSettings'
import type { SettingsSectionId } from './settings-state'

export function PreviousIntegrationSettingsNotice({ profiles, onNavigate }: {
  profiles: IntegrationProfiles
  onNavigate: (section: SettingsSectionId) => void
}) {
  const available = (['metrics', 'argocd', 'cost'] as const).filter(kind =>
    profiles[kind].state === 'auto' && profiles[kind].legacy,
  )
  if (available.length === 0) return null
  return (
    <section aria-label="Previous integration settings" className="mt-3 rounded-lg border border-theme-border bg-accent-muted p-3">
      <h4 className="text-sm font-medium text-theme-text-primary">Previous integration settings found</h4>
      <p className="mt-1 text-xs text-theme-text-secondary">Choose what to use for this cluster. Nothing is applied until you save.</p>
      <div className="mt-2 flex flex-wrap gap-x-4 gap-y-2">
        {available.map(kind => (
          <button key={kind} type="button" className="text-xs text-accent-text hover:underline"
            onClick={() => onNavigate(kind === 'metrics' ? 'prometheus' : kind)}>
            Review {integrationNames[kind]}
          </button>
        ))}
      </div>
    </section>
  )
}

export function LocalConfigurationDetails() {
  return (
    <div className="mt-5 border-t border-theme-border pt-4">
      <Disclosure
        summary="Configuration files"
        summaryClassName="text-sm font-medium text-theme-text-primary"
      >
        <div className="space-y-3 pt-2 text-xs text-theme-text-secondary">
          <p>
            CLI and Desktop share these files in <code>~/.radar/</code> on this
            machine.
          </p>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
            <dt className="font-mono text-theme-text-primary">config.json</dt>
            <dd>
              Startup defaults, such as browser, port and timeline storage.
              Flags take precedence.
            </dd>
            <dt className="font-mono text-theme-text-primary">settings.json</dt>
            <dd>Local preferences, audit settings and OCI registry sources.</dd>
            <dt className="font-mono text-theme-text-primary">clusters.json</dt>
            <dd>
              Metrics, Argo CD and Cost connections and credentials, saved per
              kubeconfig entry.
            </dd>
          </dl>
          <p className="text-theme-text-tertiary">
            Credentials are permission-protected plaintext, not encrypted.
            Connection edits from another process apply on the next integration
            operation.
          </p>
        </div>
      </Disclosure>
    </div>
  )
}

export function SavedClusterConnections({
  active,
  profiles,
  onChange,
  onBusyChange,
}: {
  active: boolean
  profiles: IntegrationProfiles
  onChange: (profiles: IntegrationProfiles) => void
  onBusyChange: (busy: boolean) => void
}) {
  const [uses, setUses] = useState<StoredConnection[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [version, setVersion] = useState(0)
  const [selected, setSelected] = useState<StoredConnection | null>(null)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const request = useRef<AbortController | null>(null)
  const summary = useRef<HTMLHeadingElement>(null)
  const trigger = useRef<HTMLButtonElement | null>(null)
  const confirmationError = useRef<HTMLParagraphElement>(null)
  const restoreFocus = useRef<'summary' | 'trigger' | null>(null)

  useEffect(() => {
    if (selected || busy || !restoreFocus.current) return
    const target = restoreFocus.current === 'trigger' && trigger.current?.isConnected
      ? trigger.current : summary.current
    restoreFocus.current = null
    target?.focus()
  }, [selected, busy])

  useEffect(() => {
    if (selected && error && !busy) confirmationError.current?.focus()
  }, [selected, error, busy])

  useEffect(() => {
    if (!active) return
    const controller = new AbortController()
    const base = getApiBase()
    setLoading(true)
    setError('')
    void fetch(apiUrl('/integrations/connections'), {
      signal: controller.signal,
      credentials: getCredentialsMode(),
      headers: getAuthHeaders(),
    })
      .then(async (response) => {
        const data = (await response.json()) as ConnectionResponse
        if (!response.ok)
          throw new Error(data.error || 'Could not load saved connections.')
        if (controller.signal.aborted || getApiBase() !== base) return
        setUses(data.connections)
      })
      .catch((error) => {
        if (!controller.signal.aborted)
          setError(error instanceof Error ? error.message : String(error))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [active, version])

  useEffect(
    () => () => {
      request.current?.abort()
      onBusyChange(false)
    },
    [onBusyChange],
  )

  const remove = async () => {
    if (!selected || request.current) return
    const controller = new AbortController()
    request.current = controller
    const base = getApiBase()
    setBusy(true)
    onBusyChange(true)
    setError('')
    try {
      const response = await fetch(apiUrl('/integrations/connections'), {
        method: 'PUT',
        signal: controller.signal,
        credentials: getCredentialsMode(),
        headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
        body: JSON.stringify({
          action: 'forget',
          kind: selected.integration,
          binding: selected.binding,
          sourceRevision: selected.revision,
          confirmRemoval: true,
        }),
      })
      const data = (await response.json()) as ConnectionResponse
      if (controller.signal.aborted || getApiBase() !== base) return
      if (!response.ok)
        throw new Error(data.error || 'Could not remove the saved connection.')
      setUses(data.connections)
      setMessage(
        `Removed the saved ${integrationNames[selected.integration]} connection for ${selected.context}.`,
      )
      restoreFocus.current = 'summary'
      setSelected(null)
      onChange(data.profiles)
    } catch (error) {
      if (!controller.signal.aborted)
        setError(error instanceof Error ? error.message : String(error))
    } finally {
      if (!controller.signal.aborted) {
        setBusy(false)
        onBusyChange(false)
      }
      if (request.current === controller) request.current = null
    }
  }

  const clusters = new Map<string, StoredConnection[]>()
  for (const use of uses) {
    const entries = clusters.get(use.binding) ?? []
    entries.push(use)
    clusters.set(use.binding, entries)
  }
  return (
    <section
      className="mt-5 space-y-3 border-t border-theme-border pt-4"
      onKeyDownCapture={(event) => {
        if (selected) trapConnectionConfirmationFocus(event)
      }}
    >
      <h4
        ref={summary}
        tabIndex={-1}
        className="text-sm font-semibold text-theme-text-primary"
      >
        Saved connections
      </h4>
      <div className="space-y-3 pt-2">
        <p className="text-xs text-theme-text-secondary">
          Connections saved for each kubeconfig entry. Configure them in
          Metrics, Argo CD or Cost.
        </p>
        {loading && (
          <p role="status" className="text-xs text-theme-text-tertiary">
            Loading saved connections…
          </p>
        )}
        {!loading && !error && clusters.size === 0 && (
          <p className="text-xs text-theme-text-tertiary">
            No saved connections yet.
          </p>
        )}
        {!loading &&
          Array.from(clusters, ([binding, entries]) => {
            const entry = entries[0]
            const current = Object.values(profiles).some(
              (profile) => profile.target.binding === binding,
            )
            return (
              <div key={binding} className="card-inner-lg space-y-2 text-xs">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium text-theme-text-primary">
                    {entry.context}
                  </span>
                  {current ? (
                    <Badge tone="note" size="sm">
                      Current
                    </Badge>
                  ) : entry.availability === 'removed' ? (
                    <Badge tone="note" size="sm">
                      Not in kubeconfig
                    </Badge>
                  ) : entry.availability === 'unavailable' ? (
                    <Badge tone="note" size="sm">
                      Not loaded in this session
                    </Badge>
                  ) : null}
                </div>
                <p className="break-all text-theme-text-tertiary">
                  {entry.source} · {entry.inFileName}
                </p>
                {!current && entry.availability === 'removed' && (
                  <p className="text-theme-text-secondary">
                    This kubeconfig entry is gone, but its connections are still
                    saved. To reuse one, select the new context and choose “Copy
                    from another cluster” in that integration’s settings.
                  </p>
                )}
                {(current ||
                  entries.every((use) => use.availability === 'available')) && (
                  <p className="text-theme-text-secondary">
                    Saved:{' '}
                    {entries
                      .map((use) => integrationNames[use.integration])
                      .join(' · ')}
                  </p>
                )}
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  {entries
                    .filter(
                      (use) => !current && use.availability !== 'available',
                    )
                    .map((use) => (
                      <div key={use.integration} className="flex items-center">
                        <button
                          type="button"
                          disabled={busy || !!selected}
                          className="text-accent-text hover:underline disabled:opacity-50"
                          aria-label={`Remove saved ${integrationNames[use.integration]} connection for ${use.context}`}
                          onClick={(event) => {
                            trigger.current = event.currentTarget
                            setSelected(use)
                            setError('')
                            setMessage('')
                          }}
                        >
                          Remove {integrationNames[use.integration]}
                        </button>
                      </div>
                    ))}
                </div>
              </div>
            )
          })}
        {message && (
          <p role="status" className="text-xs text-theme-text-secondary">
            {message}
          </p>
        )}
        {error && !selected && (
          <p role="alert" className="text-xs text-warning-text">
            {error}{' '}
            <button
              type="button"
              className="underline"
              onClick={() => setVersion((value) => value + 1)}
            >
              Reload saved connections
            </button>
          </p>
        )}
      </div>
      <ConfirmDialog
        open={!!selected}
        onClose={() => {
          restoreFocus.current = 'trigger'
          setSelected(null)
        }}
        onConfirm={() => void remove()}
        title="Remove saved connection?"
        message={
          selected
            ? `Remove the saved ${integrationNames[selected.integration]} connection and credentials for ${selected.context}? Other saved connections are unchanged. Nothing is deleted from Kubernetes or the backend.${selected.availability === 'unavailable' ? ' This entry may still exist in another kubeconfig; removal also affects any CLI or Desktop using that entry.' : ''}`
            : ''
        }
        confirmLabel="Remove connection"
        isLoading={busy}
        confirmDisabled={!!error}
        showWarning={false}
      >
        {error && (
          <p
            ref={confirmationError}
            tabIndex={-1}
            role="alert"
            className="text-sm text-warning-text"
          >
            {error} Cancel and reload saved connections before trying again.
          </p>
        )}
      </ConfirmDialog>
    </section>
  )
}
