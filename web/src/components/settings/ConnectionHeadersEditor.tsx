import { Input } from '@skyhook-io/k8s-ui'

export interface HeaderOperation {
  key: string
  action: 'keep' | 'set' | 'clear'
  value?: string
}

export function ConnectionHeadersEditor({
  keys,
  environmentKeys,
  value,
  onChange
}: {
  keys: string[]
  environmentKeys: string[]
  value: HeaderOperation[]
  onChange: (value: HeaderOperation[]) => void
}) {
  const rows = value.length
    ? value
    : keys.map((key): HeaderOperation => ({ key, action: 'keep' }))
  const update = (index: number, patch: Partial<HeaderOperation>) =>
    onChange(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)))
  return (
    <section className="mt-4 space-y-2">
      <h4 className="text-sm font-medium text-theme-text-primary">
        Authentication headers
      </h4>
      <p className="text-xs text-theme-text-tertiary">
        Optional. Saved values stay unchanged unless you replace or remove them.
      </p>
      {rows.map((row, index) => {
        const saved = index < keys.length
        const environment = saved && environmentKeys.some(
          (key) => key.toLowerCase() === row.key.toLowerCase()
        )
        return (
          <div key={index} className="space-y-1">
            <div className="flex items-center gap-2">
              <Input
                aria-label={`Header ${index + 1} name`}
                value={row.key}
                disabled={saved}
                onChange={(e) => update(index, { key: e.target.value })}
                placeholder="Header name"
                  className="min-w-0 flex-1 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
              />
              <Input
                aria-label={`${row.key || 'New header'} value`}
                type="password"
                autoComplete="new-password"
                disabled={environment || row.action === 'clear'}
                value={row.value ?? ''}
                placeholder={
                  row.action === 'clear'
                    ? 'Will be removed'
                    : environment
                      ? 'From environment'
                      : saved
                        ? 'Saved value'
                        : 'Value'
                }
                onChange={(e) =>
                  update(index, {
                    action: saved && !e.target.value ? 'keep' : 'set',
                    value: e.target.value || undefined
                  })
                }
                  className="min-w-0 flex-1 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
              />
              {!environment && (
                <button
                  type="button"
                  className="text-xs text-accent-text hover:underline shrink-0"
                  aria-label={`${row.action === 'clear' ? 'Undo removal of' : 'Remove'} ${row.key || 'new header'}`}
                  onClick={() =>
                    saved
                      ? update(index, {
                          action: row.action === 'clear' ? 'keep' : 'clear',
                          value: undefined
                        })
                      : onChange(rows.filter((_, i) => i !== index))
                  }
                >
                  {row.action === 'clear' ? 'Undo' : 'Remove'}
                </button>
              )}
            </div>
            {environment && (
              <p className="text-xs text-theme-text-tertiary">
                Environment reference · edit this header in clusters.json.
              </p>
            )}
          </div>
        )
      })}
      <button
        type="button"
        className="text-xs text-accent-text hover:underline"
        onClick={() =>
          onChange([...rows, { key: '', action: 'set', value: '' }])
        }
      >
        Add header
      </button>
    </section>
  )
}
