import { useEffect, useState } from 'react'
import { ShieldAlert } from 'lucide-react'
import { AlertBanner, Collapse, CollapseChevron, useDisclosure } from '@skyhook-io/k8s-ui'
import { useAuthMe } from '../api/client'
import { bindableGroups } from './noClusterAccess'

// Every read is filtered to the user's namespaces, so someone bound to none
// sees empty lists in every view and reads it as an empty cluster. Stated in
// terms of the user's own access, not how the cluster is meant to be set up:
// Radar can't know whether an org intends hub roles or IdP groups here.
export function NoClusterAccessBanner() {
  const { data: me, refetch } = useAuthMe()
  // auth/me is first fetched before the cluster connects, when the server
  // can't report access yet; this mounts once content is ready (and again
  // after a context switch), so ask again then.
  useEffect(() => {
    void refetch()
  }, [refetch])
  const [groupsOpen, setGroupsOpen] = useState(false)
  const groupsDisclosure = useDisclosure(groupsOpen)

  if (!me?.noNamespaceAccess) return null

  const groups = bindableGroups(me.groups ?? [])
  return (
    <div className="px-4 pt-3">
      <AlertBanner
        variant="warning"
        icon={ShieldAlert}
        title="You don't have access to anything in this cluster"
        message={
          <>
            Radar is connected, but your Kubernetes permissions don't allow reading any namespace, so
            every view looks empty. Signed in as <span className="font-mono">{me.username}</span>.
          </>
        }
      >
        {groups.length > 0 && (
          <div className="mt-2">
            <button
              {...groupsDisclosure.buttonProps}
              onClick={() => setGroupsOpen((v) => !v)}
              className="flex items-center gap-1.5 text-xs text-theme-text-secondary hover:text-theme-text-primary transition-colors"
            >
              <CollapseChevron open={groupsOpen} className="w-3.5 h-3.5" />
              Your groups ({groups.length})
            </button>
            <Collapse open={groupsOpen} id={groupsDisclosure.panelId}>
              <div className="mt-2 flex max-h-32 max-w-4xl flex-wrap gap-1.5 overflow-y-auto">
                {groups.map((g) => (
                  <span
                    key={g}
                    className="inline-flex items-center rounded-md bg-theme-elevated px-1.5 py-0.5 font-mono text-[11px] text-theme-text-secondary ring-1 ring-theme-border"
                  >
                    {g}
                  </span>
                ))}
              </div>
            </Collapse>
          </div>
        )}
      </AlertBanner>
    </div>
  )
}
