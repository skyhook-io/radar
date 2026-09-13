import { useNavCustomization } from '../../context/NavCustomization'
import { useCapabilities } from '../../api/client'
import { openCloudFunnel } from '../CloudFunnelButton'

// The one sentence a standalone Radar adds after "this application deploys
// to another cluster": the thing the user just ran into is what Radar Cloud
// does. "Radar Cloud" opens the same dialog as the top-right button, so
// there is one pitch and one connect flow. Rendered only when that button
// is: the server decides whether there is anything to pitch (cloudConnect
// capability), and embedded hosts (Radar Cloud itself) mount neither.
export function RemoteDestinationCloudHint() {
  const { embedded } = useNavCustomization()
  const capabilities = useCapabilities()
  if (embedded || !capabilities.data?.cloudConnect) return null
  return (
    <>
      Connect that cluster with{' '}
      <button type="button" onClick={openCloudFunnel} className="underline decoration-theme-border underline-offset-2 hover:text-theme-text-primary">
        Radar Cloud
      </button>{' '}
      to see this app's resources here.
    </>
  )
}
