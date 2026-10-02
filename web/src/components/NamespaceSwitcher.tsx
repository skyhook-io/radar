import { forwardRef } from 'react'
import { NamespacePicker, type NamespacePickerHandle } from '@skyhook-io/k8s-ui'
import { useAuthMe, useCapabilities, useNamespaceScope, useSetActiveNamespace } from '../api/client'

export type NamespaceSwitcherHandle = NamespacePickerHandle

const NAMESPACES_HELP_URL = 'https://radarhq.io/docs/configuration/files#namespaces-missing-from-the-picker'

interface NamespaceSwitcherProps {
  className?: string
  disabled?: boolean
  disabledTooltip?: string
  variant?: 'chip' | 'segment'
  label?: string
}

/**
 * OSS Radar's namespace scope control — a thin data container over the shared
 * presentational NamespacePicker (@skyhook-io/k8s-ui). Wires Radar's own API
 * hooks; Radar Hub supplies its own container over the per-cluster apiBase.
 */
export const NamespaceSwitcher = forwardRef<NamespaceSwitcherHandle, NamespaceSwitcherProps>(function NamespaceSwitcher(
  { className, disabled, disabledTooltip, variant, label },
  ref,
) {
  const { data: scope, isLoading } = useNamespaceScope()
  const setActive = useSetActiveNamespace()

  const { data: capabilities } = useCapabilities()
  const { data: authMe } = useAuthMe()
  // --namespaces and ~/.radar/config.json only reach a Radar the user launched
  // themselves. With auth enabled, a non-authoritative list is also the
  // per-user RBAC filter on a shared install, where neither applies.
  const canConfigureNamespaces = capabilities?.deployment?.mode === 'local' && authMe?.authEnabled === false

  const limitedListHelp = canConfigureNamespaces ? (
    <a href={NAMESPACES_HELP_URL} target="_blank" rel="noreferrer" className="text-accent-text hover:underline">
      How to add namespaces
    </a>
  ) : undefined

  return (
    <NamespacePicker
      ref={ref}
      scope={scope}
      loading={isLoading}
      pending={setActive.isPending}
      onApply={namespaces => setActive.mutate({ namespaces })}
      disabled={disabled}
      disabledTooltip={disabledTooltip}
      className={className}
      variant={variant}
      label={label}
      limitedListHelp={limitedListHelp}
    />
  )
})
