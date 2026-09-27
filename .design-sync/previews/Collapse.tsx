import { Collapse, CollapseChevron, useDisclosure, Property, PropertyList } from '@skyhook-io/k8s-ui'
import { useState, type ReactNode } from 'react'

function DrawerSection({ title, defaultOpen, children }: { title: string; defaultOpen: boolean; children: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen)
  const { panelId, buttonProps } = useDisclosure(open)
  return (
    <div style={{ width: 420 }} className="border-b-subtle pb-4">
      <button
        {...buttonProps}
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 w-full text-left hover:text-theme-text-primary transition-colors mb-2"
      >
        <CollapseChevron open={open} className="w-4 h-4" />
        <span className="font-medium text-theme-text-secondary text-sm">{title}</span>
      </button>
      <Collapse open={open} id={panelId}>
        <div className="pl-6">{children}</div>
      </Collapse>
    </div>
  )
}

export function OpenDrawerSection() {
  return (
    <DrawerSection title="Container: checkout" defaultOpen>
      <PropertyList>
        <Property label="Image" value="ghcr.io/acme/checkout:2.14.1" />
        <Property label="Ports" value="8080/TCP, 9090/TCP" />
        <Property label="Requests" value="250m CPU · 512Mi memory" />
        <Property label="Limits" value="1 CPU · 1Gi memory" />
      </PropertyList>
    </DrawerSection>
  )
}

export function ClosedDrawerSection() {
  return (
    <DrawerSection title="Annotations (6)" defaultOpen={false}>
      <PropertyList>
        <Property label="deployment.kubernetes.io/revision" value="14" />
      </PropertyList>
    </DrawerSection>
  )
}

export function AdvancedSettings() {
  const [open, setOpen] = useState(true)
  const { panelId, buttonProps } = useDisclosure(open)
  return (
    <div style={{ width: 420 }}>
      <button
        type="button"
        {...buttonProps}
        onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-1.5 rounded-md py-1 text-left text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary"
      >
        <CollapseChevron open={open} className="h-3.5 w-3.5" />
        Advanced connection settings
      </button>
      <Collapse open={open} id={panelId}>
        <div className="pt-3">
          <div className="space-y-1 rounded-md border border-theme-border-subtle bg-theme-base/60 p-3 text-xs text-theme-text-secondary">
            <div className="text-sm font-medium text-theme-text-primary">Kubecost URL</div>
            <div>Leave blank when Kubecost runs in this cluster. Enter the central Aggregator URL for a federated setup.</div>
          </div>
        </div>
      </Collapse>
    </div>
  )
}
