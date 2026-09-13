import { useNavCustomization } from '../../context/NavCustomization'
import { useCapabilities } from '../../api/client'
import { cloudSignupUrl } from '../CloudFunnelButton'

// The one sentence a standalone Radar adds after "this application deploys
// to another cluster": the thing the user just ran into is what Radar Cloud
// does. Embedded hosts (Radar Cloud itself) render nothing — they already
// are the answer. No modal, no state, no repeat nag: it is a link.
export function RemoteDestinationCloudHint() {
  const { embedded } = useNavCustomization()
  const capabilities = useCapabilities()
  if (embedded) return null
  const href = cloudSignupUrl(capabilities.data?.cloudConnect?.appUrl, 'gitops-remote-destination')
  return (
    <>
      <a href={href} target="_blank" rel="noreferrer" className="underline decoration-theme-border underline-offset-2 hover:text-theme-text-primary">
        Radar Cloud
      </a>{' '}
      connects the destination cluster and shows this application's resources there.
    </>
  )
}
