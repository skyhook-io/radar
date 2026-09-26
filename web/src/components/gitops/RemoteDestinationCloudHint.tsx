import { CloudHintLink } from '../cloudHints/CloudHintLink'
import { useCloudHintsEnabled } from '../cloudHints/cloudHints'

// The one sentence a standalone Radar adds after "this application deploys
// to another cluster": the thing the user just ran into is what Radar Cloud
// does. "Radar Cloud" opens the same dialog as the top-right button, so
// there is one pitch and one connect flow. That dialog connects THIS
// cluster; the destination is a second connect from Cloud, which is why the
// copy says "both clusters" and promises the view in Cloud, not here.
export function RemoteDestinationCloudHint() {
  const enabled = useCloudHintsEnabled()
  if (!enabled) return null
  return (
    <>
      Connect both clusters to <CloudHintLink entry="gitops-remote" /> to see this app's resources together.
    </>
  )
}
