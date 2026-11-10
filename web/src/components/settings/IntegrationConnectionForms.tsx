import { useEffect, useId, useState, type ReactNode } from 'react'
import { Input, Disclosure, SelectMenu } from '@skyhook-io/k8s-ui'
import { FormSaveActions, type FormFeedback } from './FormSaveActions'
import { COST_SOURCE_OPTIONS } from '../cost/source'

export interface SecretEdit {
  action: 'keep' | 'set' | 'clear'
  value?: string
}
export interface ArgoConnectionDraft {
  url: string
  insecureTls: boolean
  secret?: SecretEdit
  useCliToken?: boolean
}
export interface CostConnectionDraft {
  url: string
  mode: 'auto' | 'prometheus' | 'kubecost'
  clusterId: string
  secret?: SecretEdit
}

function CredentialField({
  label,
  saved,
  value,
  onChange
}: {
  label: string
  saved: boolean
  value: SecretEdit
  onChange: (value: SecretEdit) => void
}) {
  const id = useId()
  return (
    <section className="space-y-2 border-t border-theme-border pt-4">
      <label
        htmlFor={id}
        className="block text-sm font-medium text-theme-text-primary"
      >
        {label}
      </label>
      <div className="flex items-center gap-2">
        <Input
          id={id}
          type="password"
          autoComplete="new-password"
          spellCheck={false}
          value={value.value ?? ''}
          onChange={(e) =>
            onChange({
              action: e.target.value ? 'set' : 'keep',
              value: e.target.value || undefined
            })
          }
          placeholder={
            value.action === 'clear'
              ? 'Will be removed'
              : saved
                ? 'Saved value'
                : 'Optional'
          }
          className="block w-full min-w-0 px-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500 flex-1"
        />
        {saved && (
          <button
            type="button"
            className="text-xs text-accent-text hover:underline"
            onClick={() =>
              onChange(
                value.action === 'keep'
                  ? { action: 'clear' }
                  : { action: 'keep' }
              )
            }
          >
            {value.action === 'keep' ? 'Remove' : 'Undo'}
          </button>
        )}
      </div>
      <p className="text-xs text-theme-text-tertiary">
        {value.action === 'clear'
          ? 'Will be removed when you save.'
          : 'Leave unchanged to keep the saved value.'}
      </p>
    </section>
  )
}

export function ArgoCDConnectionForm({
  value,
  secretSet,
  cliSession,
  onChange,
  onApply,
  dirty,
  onDiscard,
  connectionAction,
  feedback,
  onDirtyChange
}: {
  value: ArgoConnectionDraft
  secretSet: boolean
  cliSession?: { server: string; user: string; insecure?: boolean }
  onChange: (value: ArgoConnectionDraft) => void
  onApply: (value: ArgoConnectionDraft) => Promise<void | boolean>
  dirty: boolean
  onDiscard: () => void
  connectionAction?: ReactNode
  feedback?: FormFeedback
  onDirtyChange?: (dirty: boolean) => void
}) {
  const id = useId()
  const [secret, setSecret] = useState<SecretEdit>({ action: 'keep' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(
    () => onDirtyChange?.(secret.action !== 'keep'),
    [secret, onDirtyChange]
  )
  const apply = async (useCliToken = false) => {
    setBusy(true)
    setError('')
    try {
      const applied = await onApply({
        ...value,
        ...(useCliToken ? { useCliToken: true } : { secret })
      })
      if (applied === false) return
      setSecret({ action: 'keep' })
    } catch (error) {
      setError(error instanceof Error ? error.message : String(error))
    } finally {
      setBusy(false)
    }
  }
  return (
    <fieldset disabled={busy} className="min-w-0 space-y-4">
      <p className="text-sm text-theme-text-secondary">
        Compare Git configuration with live resources and show Argo CD health.
        Auto-discovery works without setup where anonymous reads are allowed.
      </p>
      <div className="space-y-1">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <label
            htmlFor={id}
            className="block text-sm font-medium text-theme-text-primary"
          >
            Argo CD server URL
          </label>
        </div>
        <p className="text-xs text-theme-text-tertiary">
          Reachable from Radar. Leave empty to discover the server in this
          cluster.
        </p>
        <Input
          className="block w-full min-w-0 px-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
          id={id}
          value={value.url}
          onChange={(e) => onChange({ ...value, url: e.target.value })}
          placeholder="Auto-discover, or https://argocd.example.com"
        />
        {connectionAction}
      </div>
      <CredentialField
        label="API token"
        saved={secretSet}
        value={secret}
        onChange={setSecret}
      />
      {cliSession && (
        <div className="text-xs text-theme-text-secondary space-y-1">
          <p>
            CLI session available: {cliSession.user} at {cliSession.server}
          </p>
          <button
            type="button"
            disabled={!value.url.trim()}
            onClick={() => void apply(true)}
            className="text-accent-text hover:underline disabled:opacity-50"
          >
            Connect using CLI token
          </button>
        </div>
      )}
      <label className="flex items-center gap-2 text-xs text-theme-text-secondary">
        <input
          type="checkbox"
          checked={value.insecureTls}
          onChange={(e) =>
            onChange({ ...value, insecureTls: e.target.checked })
          }
        />
        Skip TLS verification (self-signed server)
      </label>
      {value.insecureTls && (
        <p className="text-xs text-warning-text">
          Server identity will not be verified. Use only for a trusted server.
        </p>
      )}
      {value.url.startsWith('http://') &&
        (secretSet || secret.action === 'set') && (
          <p className="text-xs text-warning-text">
            Credentials will travel over unencrypted HTTP. Prefer HTTPS outside
            a trusted private network.
          </p>
        )}
      <FormSaveActions
        dirty={dirty}
        busy={busy}
        onSave={() => void apply()}
        onDiscard={onDiscard}
        feedback={feedback}
        error={error}
      />
    </fieldset>
  )
}

export function CostConnectionForm({
  value,
  secretSet,
  onChange,
  onApply,
  dirty,
  onDiscard,
  connectionAction,
  feedback,
  onDirtyChange
}: {
  value: CostConnectionDraft
  secretSet: boolean
  onChange: (value: CostConnectionDraft) => void
  onApply: (value: CostConnectionDraft) => Promise<void | boolean>
  dirty: boolean
  onDiscard: () => void
  connectionAction?: ReactNode
  feedback?: FormFeedback
  onDirtyChange?: (dirty: boolean) => void
}) {
  const id = useId()
  const [secret, setSecret] = useState<SecretEdit>({ action: 'keep' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [overridesOpen, setOverridesOpen] = useState(
    !!value.url || !!value.clusterId || secretSet
  )
  const automatic = value.mode === 'auto'
  useEffect(
    () => onDirtyChange?.(secret.action !== 'keep'),
    [secret, onDirtyChange]
  )
  const apply = async () => {
    setBusy(true)
    setError('')
    try {
      const applied = await onApply({ ...value, secret })
      if (applied === false) return
      setSecret({ action: 'keep' })
    } catch (error) {
      setError(error instanceof Error ? error.message : String(error))
      setOverridesOpen(true)
    } finally {
      setBusy(false)
    }
  }
  return (
    <fieldset disabled={busy} className="min-w-0 space-y-4">
      <p className="text-sm text-theme-text-secondary">
        Use OpenCost metrics from this cluster's metrics connection, or connect
        to a Kubecost Aggregator.
      </p>
      <div className="space-y-1">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <label
            htmlFor={`${id}-source`}
            className="text-sm font-medium text-theme-text-primary"
          >
            Cost source
          </label>
        </div>
        <SelectMenu
          id={`${id}-source`}
          ariaLabel="Cost source"
          value={value.mode}
          options={COST_SOURCE_OPTIONS}
          onChange={(mode) => {
            if (
              mode === 'auto' &&
              (value.url ||
                value.clusterId ||
                secretSet ||
                secret.action !== 'keep')
            )
              setOverridesOpen(true)
            onChange({
              ...value,
              mode: mode as CostConnectionDraft['mode']
            })
          }}
          className="w-full"
        />
        {(value.mode === 'prometheus' || (automatic && !overridesOpen)) && connectionAction}
      </div>
      {value.mode !== 'prometheus' && (
        <Disclosure
          summary={
            value.url ||
            value.clusterId ||
            secretSet ||
            secret.action !== 'keep'
              ? 'Kubecost connection overrides · configured'
              : 'Kubecost connection overrides (optional)'
          }
          summaryClassName={
            automatic
              ? 'text-sm font-medium text-theme-text-secondary'
              : 'hidden'
          }
          open={!automatic || overridesOpen}
          onOpenChange={setOverridesOpen}
        >
          <div className={automatic ? 'space-y-4 pt-3' : 'space-y-4'}>
            <div className="space-y-1">
              <label
                htmlFor={id}
                className="block text-sm font-medium text-theme-text-primary"
              >
                Kubecost Aggregator URL
              </label>
              <p className="text-xs text-theme-text-tertiary">
                Leave empty for discovery in this cluster. Use the central
                Aggregator URL for a federated setup.
              </p>
              <Input
                className="block w-full min-w-0 px-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                id={id}
                value={value.url}
                onChange={(e) => onChange({ ...value, url: e.target.value })}
                placeholder="Auto-discover, or https://kubecost.example.com"
              />
              {(!automatic || overridesOpen) && connectionAction}
            </div>
            <CredentialField
              label="API key"
              saved={secretSet}
              value={secret}
              onChange={setSecret}
            />
            {value.url.startsWith('http://') &&
              (secretSet || secret.action === 'set') && (
                <p className="text-xs text-warning-text">
                  The API key will travel over unencrypted HTTP. Prefer HTTPS
                  outside a trusted private network.
                </p>
              )}
            <section className="space-y-1 border-t border-theme-border pt-4">
              <label
                htmlFor={`${id}-cluster`}
                className="block text-sm font-medium text-theme-text-primary"
              >
                Kubecost cluster ID
              </label>
              <p className="text-xs text-theme-text-tertiary">
                The FinOps Agent's CLUSTER_ID, not the kubeconfig context
                name. Leave empty to detect it. Applies only to this cluster.
              </p>
              <Input
                className="block w-full min-w-0 px-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                id={`${id}-cluster`}
                value={value.clusterId}
                onChange={(e) =>
                  onChange({ ...value, clusterId: e.target.value })
                }
                placeholder="Detected automatically"
              />
            </section>
          </div>
        </Disclosure>
      )}
      <FormSaveActions
        dirty={dirty}
        busy={busy}
        onSave={() => void apply()}
        onDiscard={onDiscard}
        feedback={feedback}
        error={error}
      />
    </fieldset>
  )
}
