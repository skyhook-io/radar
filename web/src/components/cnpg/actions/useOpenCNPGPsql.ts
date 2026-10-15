import { useOpenTerminal } from '../../dock'

/**
 * Opens psql in an instance's postgres container in the dock terminal, over
 * the caller's own pods/exec. It connects on the local socket as the postgres
 * superuser, like `kubectl cnpg psql`.
 */
export function useOpenCNPGPsql() {
  const openTerminal = useOpenTerminal()
  return (namespace: string, pod: string, isPrimary: boolean) =>
    openTerminal({
      namespace,
      podName: pod,
      containerName: 'postgres',
      containers: ['postgres'],
      shell: 'psql',
      title: `psql · ${pod}`,
      sessionNote: isPrimary
        ? 'psql as postgres (superuser) · currently the primary: read-write'
        : 'psql as postgres (superuser) · currently a standby: read-only while it stays a standby',
    })
}
