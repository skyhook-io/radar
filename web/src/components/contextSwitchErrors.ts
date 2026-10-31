// One plain sentence for a context switch Radar refused because the new
// cluster didn't answer. The person is still on their current cluster, so
// this is a line under the picker, not the full connection error screen.
// errorType is the server's ClassifyError vocabulary.
export function contextSwitchErrorText(errorType: string, cluster: string): string {
  switch (errorType) {
    case 'network':
      return `Can't reach ${cluster}. Check your VPN or network.`
    case 'timeout':
      return `${cluster} didn't respond in time. Check your VPN or network.`
    case 'auth':
    case 'auth-rejected':
      return `Sign-in to ${cluster} failed. Refresh your credentials and try again.`
    case 'auth-plugin-stuck':
      return `The sign-in plugin for ${cluster} didn't respond.`
    case 'tls':
      return `Certificate problem connecting to ${cluster}.`
    case 'rbac':
      return `Your account isn't allowed to read ${cluster}.`
    case 'config':
      return `Radar couldn't load ${cluster} from your kubeconfig.`
    default:
      return `Couldn't connect to ${cluster}.`
  }
}
