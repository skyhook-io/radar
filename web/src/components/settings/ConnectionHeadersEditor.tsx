import { useState } from 'react'
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
  const [editing, setEditing] = useState(false)
  const rows = value.length
    ? value
    : keys.map((key): HeaderOperation => ({ key, action: 'keep' }))
  const update = (index: number, patch: Partial<HeaderOperation>) =>
    onChange(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)))
  return (
    <section className="mt-5 border-t border-theme-border pt-4 space-y-3">
      <h4 className="text-sm font-medium text-theme-text-primary">
        Authentication headers
      </h4>
      <p className="text-xs text-theme-text-tertiary">
        Optional authentication or tenant headers, such as Authorization or
        X-Scope-OrgID. Requires a backend URL.
      </p>
      {!editing ? (
        <div className="flex justify-between gap-3 text-xs">
          <span className="text-theme-text-secondary break-words">
            {keys.length
              ? `${keys.join(', ')} (values hidden)`
              : 'No headers configured'}
          </span>
          <button
            type="button"
            className="text-accent-text shrink-0 hover:underline"
            onClick={() => {
              if (!keys.length)
                onChange([{ key: '', action: 'set', value: '' }])
              setEditing(true)
            }}
          >
            {keys.length ? 'Edit headers' : 'Add auth headers'}
          </button>
        </div>
      ) : (
        <div className="space-y-3">
          {rows.map((row, index) => {
            const saved = keys.some(
              (key) => key.toLowerCase() === row.key.toLowerCase()
            )
            const environment = environmentKeys.some(
              (key) => key.toLowerCase() === row.key.toLowerCase()
            )
            return (
              <div className="space-y-1" key={index}>
                <div className="flex items-center gap-2">
                  <Input
                    aria-label={`Header ${index + 1} name`}
                    value={row.key}
                    disabled={saved}
                    onChange={(e) => update(index, { key: e.target.value })}
                    placeholder="Header name"
                    className="block w-full min-w-0 px-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500 flex-1"
                  />
                  <select
                    aria-label={`${row.key || 'New header'} action`}
                    value={row.action}
                    disabled={environment}
                    onChange={(e) => {
                      if (!saved && e.target.value === 'clear')
                        onChange(rows.filter((_, i) => i !== index))
                      else
                        update(index, {
                          action: e.target.value as HeaderOperation['action'],
                          value: undefined
                        })
                    }}
                    className="rounded-md border border-theme-border bg-theme-elevated px-2 py-2 text-xs text-theme-text-primary"
                  >
                    {saved && <option value="keep">Keep saved</option>}
                    <option value="set">
                      {saved ? 'Replace' : 'Set value'}
                    </option>
                    <option value="clear">Remove</option>
                  </select>
                </div>
                {row.action === 'set' && (
                  <Input
                    className="block w-full min-w-0 px-3 py-2 text-sm bg-theme-elevated border border-theme-border rounded-md text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:border-skyhook-500"
                    type="password"
                    autoComplete="new-password"
                    aria-label={`${row.key || 'New header'} value`}
                    value={row.value ?? ''}
                    onChange={(e) => update(index, { value: e.target.value })}
                    placeholder="Value (never returned by Radar)"
                  />
                )}
                {environment && (
                  <p className="text-xs text-theme-text-tertiary">
                    Environment reference · edit this header in clusters.json.
                  </p>
                )}
              </div>
            )
          })}
          <div className="flex justify-between text-xs">
            <button
              type="button"
              className="text-accent-text hover:underline"
              onClick={() =>
                onChange([...rows, { key: '', action: 'set', value: '' }])
              }
            >
              Add header
            </button>
            <button
              type="button"
              className="text-theme-text-secondary hover:underline"
              onClick={() => {
                onChange([])
                setEditing(false)
              }}
            >
              Cancel header edits
            </button>
          </div>
          <p className="text-xs text-theme-text-tertiary">
            Unchanged values stay saved; no need to reenter them. Changes apply
            with the connection.
          </p>
        </div>
      )}
    </section>
  )
}
