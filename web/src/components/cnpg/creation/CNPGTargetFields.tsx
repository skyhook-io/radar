import { useId, type InputHTMLAttributes } from 'react'
import { Input } from '@skyhook-io/k8s-ui'
import type { CNPGTarget } from './targetModel'
import { CNPG_FORM_FIELD as FIELD } from '../formFields'

export function CNPGTargetFields({
  value,
  onChange,
  idPrefix,
  namespaces = [],
  storageClasses = [],
  namespaceFixed = false,
  imageDescription,
  imageRequired = false,
  defaultClassDisabled = false,
}: {
  value: CNPGTarget
  onChange: (value: CNPGTarget) => void
  idPrefix: string
  namespaces?: string[]
  storageClasses?: string[]
  namespaceFixed?: boolean
  imageDescription?: string
  imageRequired?: boolean
  defaultClassDisabled?: boolean
}) {
  const listId = useId()
  const set = (key: keyof CNPGTarget, next: string) => onChange({ ...value, [key]: next })
  const input = (key: keyof CNPGTarget, label: string, props: InputHTMLAttributes<HTMLInputElement> = {}) => (
    <label className="block" htmlFor={`${idPrefix}-${key}`}>
      <span className="text-xs font-medium text-theme-text-secondary">{label}</span>
      <Input
        id={`${idPrefix}-${key}`}
        value={value[key]}
        onChange={(event) => set(key, event.target.value)}
        autoComplete="off"
        spellCheck={false}
        className={FIELD}
        {...props}
      />
    </label>
  )
  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2">
        {input('name', 'Cluster name', { placeholder: 'orders-db' })}
        {namespaceFixed ? (
          <div>
            <span className="text-xs font-medium text-theme-text-secondary">Namespace</span>
            <div className="mt-2 font-mono text-sm text-theme-text-primary">{value.namespace}</div>
          </div>
        ) : (
          input('namespace', 'Namespace', { list: `${listId}-namespaces`, placeholder: 'Choose a namespace' })
        )}
        {input('instances', 'Instances', { type: 'number', min: 1, step: 1 })}
        {input('size', 'Data volume size · per instance', { placeholder: '20Gi' })}
      </div>
      <p className="text-xs text-theme-text-tertiary">
        One instance is the primary; the others are standbys. Choose storage for the data you expect to keep.
      </p>
      <div className="grid items-start gap-3 sm:grid-cols-2">
        <div>
          {input('storageClass', 'Storage class · optional', {
            list: `${listId}-storage`,
            placeholder: defaultClassDisabled ? 'No default class' : 'Cluster default',
          })}
          {defaultClassDisabled && !value.storageClass && (
            <p className="mt-1 text-xs text-theme-text-tertiary">
              The PVC template opts out of the default class. Keep empty to preserve that choice, or choose a class.
            </p>
          )}
        </div>
        <div>
          {imageDescription ? (
            <div>
              <span className="text-xs font-medium text-theme-text-secondary">Recovery image</span>
              <p className="mt-1 break-all font-mono text-sm text-theme-text-primary">{imageDescription}</p>
              <p className="mt-1 text-xs text-theme-text-tertiary">
                Physical recovery requires the PostgreSQL major used by the backup. Advanced YAML exposes the full configuration.
              </p>
            </div>
          ) : (
            <>
              {input(
                'image',
                imageRequired ? 'PostgreSQL image · required for recovery' : 'PostgreSQL image · optional',
                { placeholder: imageRequired ? 'Image matching the source PostgreSQL major' : 'Operator default' },
              )}
              <p className="text-xs text-theme-text-tertiary">
                {imageRequired
                  ? 'The source image was not readable. Set an image for the same PostgreSQL major before restoring.'
                  : 'Leave this empty to use the image selected by the operator.'}
              </p>
            </>
          )}
        </div>
      </div>
      <datalist id={`${listId}-namespaces`}>
        {namespaces.map((name) => (
          <option key={name} value={name} />
        ))}
      </datalist>
      <datalist id={`${listId}-storage`}>
        {storageClasses.map((name) => (
          <option key={name} value={name} />
        ))}
      </datalist>
    </div>
  )
}
