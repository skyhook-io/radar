import type { ConnectionStateType } from '../context/ConnectionContext'

// utm_content values the Hub reads to tell the driver lane's exits apart.
// Each one names a different situation, so the funnel report can separate
// "prefers the browser" from "the in-app path broke" from "there was no
// cluster to connect yet".
export type DriverEscapeContent = 'driver-alt' | 'driver-escape' | 'driver-unconnected'

// A prepare 503 is the server saying it has no cluster right now, which can
// arrive before the connection feed has noticed the drop. Until that feed
// changes (or the context does), the server's word wins over the stale
// render-time state, so the footer does not re-offer an attempt that would
// only fail again.
export function effectiveConnectionState(
  connectionState: ConnectionStateType,
  serverReportedNoCluster: boolean,
): ConnectionStateType {
  return serverReportedNoCluster ? 'disconnected' : connectionState
}

export function driverEscapeContent(connectionState: ConnectionStateType, prepareFailed: boolean): DriverEscapeContent {
  if (connectionState !== 'connected') return 'driver-unconnected'
  return prepareFailed ? 'driver-escape' : 'driver-alt'
}

// The in-app connect inspects the live cluster, so it has nothing to do until
// Radar is connected to one. Until then the browser path is the only way
// forward, and this text (shown on hover) says why instead of offering a
// button that would only produce an error.
export function driverConnectUnavailableNote(connectionState: ConnectionStateType): string | null {
  switch (connectionState) {
    case 'connected':
      return null
    case 'connecting':
      return 'Radar is still connecting to this cluster. The in-app connect appears once it is ready, or continue in Radar Cloud now.'
    case 'disconnected':
      return "Radar isn't connected to a cluster, so it can't install the connection for you. Connect a cluster first, or continue in Radar Cloud."
  }
}
