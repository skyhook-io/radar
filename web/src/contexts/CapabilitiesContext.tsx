import { createContext, useContext, ReactNode } from 'react'
import { useCapabilities } from '../api/client'
import type { Capabilities } from '../types'

// Default capabilities for local development (when running locally, all features work)
const defaultCapabilities: Capabilities = {
  exec: true,
  logs: true,
  portForward: true,
  secrets: true,
  helmWrite: true,
}

// Restricted capabilities for error/failure cases (fail-closed)
const restrictedCapabilities: Capabilities = {
  exec: false,
  logs: false,
  portForward: false,
  secrets: false,
  helmWrite: false,
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

export function useCanHelmWrite(): boolean {
  return useContext(CapabilitiesContext).helmWrite
}
