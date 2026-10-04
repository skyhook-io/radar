import { useEffect, useState } from 'react'
import { ShieldAlert } from 'lucide-react'
import { AlertBanner, Badge, Collapse, CollapseChevron, useDisclosure } from '@skyhook-io/k8s-ui'
import { useAuthMe, useNamespaceAccess } from '../api/client'

// Every read is filtered to the user's namespaces, so someone bound to none
// sees empty lists in every view and reads it as an empty cluster. Stated in
// terms of the user's own access, not how the cluster is meant to be set up:
// Radar can't know whether an org intends hub roles or IdP groups here.
export function NoClusterAccessBanner() {
  const { data: auth } = useAuthMe()
  // Mounted once content is ready (and again after a context switch), so the
  // cluster is connected by the time this asks.
  const { data } = useNamespaceAccess(Boolean(auth?.authEnabled))
  // Keep the last definite answer through a check that couldn't decide, so a
  // transient discovery failure doesn't hide the banner from a user without access.
  const [me, setMe] = useState<typeof data>()
  useEffect(() => {
    if (data && typeof data.noNamespaceAccess === 'boolean') setMe(data)
  }, [data])
  const [groupsOpen, setGroupsOpen] = useState(false)
  const groupsDisclosure = useDisclosure(groupsOpen)

  if (!me?.noNamespaceAccess) return null

  const groups = me.groups ?? []
  return (
    <div className="px-4 pt-3">
      <AlertBanner
        variant="warning"
        icon={ShieldAlert}
        title="You can't read any namespace in this cluster"
        message={
          <>
            Radar is connected, but your Kubernetes permissions don't allow reading any namespace, so
            every view looks empty. Signed in as <span className="font-mono">{me.username}</span>.
          </>
        }
      >
        {groups.length > 0 && (
          // ph-no-capture: group IDs name the customer's directory structure.
          // Radar Cloud embeds this view with session replay on, and its text
          // mask only catches credential-shaped values. A no-op without PostHog.
          <div className="ph-no-capture mt-2">
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
                  <Badge key={g} tone="structural" size="sm" className="font-mono">
                    {g}
                  </Badge>
                ))}
              </div>
            </Collapse>
          </div>
        )}
      </AlertBanner>
    </div>
  )
}
