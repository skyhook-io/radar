import { createContext, useContext, useMemo, ReactNode } from 'react'
import { useCapabilities, useNamespaceCapabilities } from '../api/client'
import type { Capabilities, ResourcePermissions } from '../types'

// Default capabilities for local development (when running locally, all features work)
const defaultCapabilities: Capabilities = {
  exec: true,
  localTerminal: true,
  logs: true,
  portForward: true,
  secrets: true,
  secretsUpdate: true,
  helmWrite: true,
  nodeWrite: true,
  mcpEnabled: true,
  // Default to 'local' for the loading window so the UI renders the
  // OSS standalone shape until /api/capabilities resolves. Both
  // alternatives ('in-cluster', 'cloud') would cause OSS users to
  // briefly see suppressed chrome — wrong default direction.
  deployment: { mode: 'local' },
}

// Restricted capabilities for error/failure cases (fail-closed)
const restrictedCapabilities: Capabilities = {
  exec: false,
  localTerminal: false,
  logs: false,
  portForward: false,
  secrets: false,
  secretsUpdate: false,
  helmWrite: false,
  nodeWrite: false,
  mcpEnabled: false,
  deployment: { mode: 'local' },
}

const CapabilitiesContext = createContext<Capabilities>(defaultCapabilities)

export function CapabilitiesProvider({ children }: { children: ReactNode }) {
  const { data: capabilities, error } = useCapabilities()

  // Determine which capabilities to use:
  // 1. If we have fetched capabilities, use them
  // 2. If there's an error, use restricted (fail-closed)
  // 3. If still loading, use defaults (assumes local dev where everything works)
  let value: Capabilities
  if (capabilities) {
    value = capabilities
  } else if (error) {
    // Log error for debugging and use restricted capabilities
    console.error('Failed to fetch capabilities, using restricted mode:', error)
    value = restrictedCapabilities
  } else {
    // Still loading - use defaults for smooth UX
    value = defaultCapabilities
  }

  return (
    <CapabilitiesContext.Provider value={value}>
      {children}
    </CapabilitiesContext.Provider>
  )
}

export function useCapabilitiesContext(): Capabilities {
  return useContext(CapabilitiesContext)
}

// Convenience hooks for specific capabilities
export function useCanExec(): boolean {
  return useContext(CapabilitiesContext).exec
}

export function useCanViewLogs(): boolean {
  return useContext(CapabilitiesContext).logs
}

export function useCanPortForward(): boolean {
  return useContext(CapabilitiesContext).portForward
}

export function useCanViewSecrets(): boolean {
  return useContext(CapabilitiesContext).secrets
}

export function useCanUpdateSecrets(): boolean {
  return useContext(CapabilitiesContext).secretsUpdate
}

export function useCanHelmWrite(): boolean {
  return useContext(CapabilitiesContext).helmWrite
}

export function useCanNodeWrite(): boolean {
  return useContext(CapabilitiesContext).nodeWrite
}

// RBAC resource permission hooks
export function useResourcePermissions(): ResourcePermissions | undefined {
  return useContext(CapabilitiesContext).resources
}

export function useRestrictedResources(): string[] {
  const resources = useContext(CapabilitiesContext).resources
  return useMemo(() => {
    if (!resources) return []
    return Object.entries(resources)
      .filter(([, allowed]) => !allowed)
      .map(([kind]) => kind)
  }, [resources])
}

export function useHasLimitedAccess(): boolean {
  const resources = useContext(CapabilitiesContext).resources
  if (!resources) return false
  return Object.values(resources).some(allowed => !allowed)
}

// Namespace-scoped capability hooks: lazily re-check exec/logs/portForward
// scoped to a specific namespace when global RBAC checks denied them.
// Falls back to global capability values while the namespace check is loading
// or when all capabilities are already granted.
export function useNamespacedCapabilities(namespace: string | undefined) {
  const globalCaps = useContext(CapabilitiesContext)
  const { data: nsCaps, error } = useNamespaceCapabilities(namespace, globalCaps)

  if (error) {
    console.warn(`Failed to fetch namespace capabilities for ${namespace}, using global:`, error)
  }

  return useMemo(() => ({
    canExec: nsCaps?.exec ?? globalCaps.exec,
    canViewLogs: nsCaps?.logs ?? globalCaps.logs,
    canPortForward: nsCaps?.portForward ?? globalCaps.portForward,
  }), [globalCaps.exec, globalCaps.logs, globalCaps.portForward, nsCaps])
}
