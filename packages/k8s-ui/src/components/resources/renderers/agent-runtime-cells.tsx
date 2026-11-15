// Agent runtime cell components for ResourcesView table

import { clsx } from 'clsx'
import type { StatusBadge } from '../resource-utils'
import { Tooltip } from '../../ui/Tooltip'
import {
  formatSandboxExpiry,
  getSandboxClaimSandbox,
  getSandboxClaimStatus,
  getSandboxClaimWarmPool,
  getSandboxExpiry,
  getSandboxNode,
  getSandboxOperatingMode,
  getSandboxStatus,
  getSandboxWarmPoolReady,
  getSandboxWarmPoolStatus,
  getSandboxWarmPoolTemplate,
  getWorkerPoolSandboxClass,
  getWorkerPoolSandboxConfig,
  getWorkerPoolStatus,
  getWorkerPoolWorkers,
} from '../resource-utils-agent-runtimes'

function StatusCell({ status }: { status: StatusBadge }) {
  return <span className={clsx('badge', status.color)}>{status.text}</span>
}

function TextCell({ value }: { value: string }) {
  if (!value || value === '-') return <span className="text-sm text-theme-text-tertiary">-</span>
  return (
    <Tooltip content={value}>
      <span className="text-sm text-theme-text-secondary truncate block">{value}</span>
    </Tooltip>
  )
}

function ExpiryCell({ value }: { value: string | undefined }) {
  if (!value) return <span className="text-sm text-theme-text-tertiary">-</span>
  return (
    <Tooltip content={new Date(value).toLocaleString()}>
      <span className="text-sm text-theme-text-secondary">{formatSandboxExpiry(value)}</span>
    </Tooltip>
  )
}

function Empty() {
  return <span className="text-sm text-theme-text-tertiary">-</span>
}

export function WorkerPoolCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': return <StatusCell status={getWorkerPoolStatus(resource)} />
    case 'workers': return <TextCell value={getWorkerPoolWorkers(resource)} />
    case 'sandboxClass': return <TextCell value={getWorkerPoolSandboxClass(resource)} />
    case 'sandboxConfig': return <TextCell value={getWorkerPoolSandboxConfig(resource)} />
    default: return <Empty />
  }
}

export function SandboxCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': return <StatusCell status={getSandboxStatus(resource)} />
    case 'operatingMode': return <TextCell value={getSandboxOperatingMode(resource)} />
    case 'sandboxNode': return <TextCell value={getSandboxNode(resource)} />
    case 'expires': return <ExpiryCell value={getSandboxExpiry(resource)} />
    default: return <Empty />
  }
}

export function SandboxClaimCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': return <StatusCell status={getSandboxClaimStatus(resource)} />
    case 'warmPool': return <TextCell value={getSandboxClaimWarmPool(resource)} />
    case 'claimedSandbox': return <TextCell value={getSandboxClaimSandbox(resource)} />
    case 'expires': return <ExpiryCell value={getSandboxExpiry(resource)} />
    default: return <Empty />
  }
}

export function SandboxWarmPoolCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': return <StatusCell status={getSandboxWarmPoolStatus(resource)} />
    case 'warmReady': return <TextCell value={getSandboxWarmPoolReady(resource)} />
    case 'template': return <TextCell value={getSandboxWarmPoolTemplate(resource)} />
    default: return <Empty />
  }
}
