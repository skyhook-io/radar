import { ArrowUpCircle } from 'lucide-react'
import { gitOpsRouteForResource } from '@skyhook-io/k8s-ui'
import type { CloudConnectSelf, VersionInfo } from '../../api/client'
import {
  getVersionUpdateStatus,
  IN_CLUSTER_UPGRADE_URL,
  type VersionUpdateTier,
} from '../../utils/version'
import { Tooltip } from '../ui/Tooltip'

interface RadarVersionLineProps {
  version: VersionInfo
  manager?: CloudConnectSelf
  managerLoading?: boolean
  onNavigateToHelmRelease?: (namespace: string, release: string) => void
  onNavigateToGitOps?: (path: string) => void
}

function displayVersion(version: string): string {
  return version === 'dev' || version.startsWith('v') ? version : `v${version}`
}

export function RadarVersionLine({
  version,
  manager,
  managerLoading = false,
  onNavigateToHelmRelease,
  onNavigateToGitOps,
}: RadarVersionLineProps) {
  const latestVersion = version.latestVersion
  const updateStatus = getVersionUpdateStatus(version.currentVersion, latestVersion)
  const showUpgrade = version.updateAvailable && !!latestVersion && updateStatus.tier !== 'none'

  if (!showUpgrade) {
    return <span>Radar <span className="font-mono">{displayVersion(version.currentVersion)}</span></span>
  }

  const controller = manager?.controllerRef
  const gitOpsPath = controller
    ? gitOpsRouteForResource({
        apiVersion: controller.group ? `${controller.group}/v1` : undefined,
        kind: controller.kind,
        metadata: { namespace: controller.namespace, name: controller.name },
      })
    : null

  let detail = managerLoading
    ? 'Checking how this installation is managed.'
    : 'The installation manager could not be confirmed. Open the in-cluster upgrade instructions.'
  let onClick: (() => void) | undefined
  const actionClassName = `inline-flex items-center gap-1 transition-colors ${upgradeActionClassName(updateStatus.tier)}`

  if (manager?.ownership === 'helm' && manager.namespace && manager.release) {
    if (onNavigateToHelmRelease) {
      detail = `Managed by Helm release ${manager.namespace}/${manager.release}. Open the release to upgrade.`
      onClick = () => onNavigateToHelmRelease(manager.namespace!, manager.release!)
    } else {
      detail = `Managed by Helm release ${manager.namespace}/${manager.release}. Open the in-cluster upgrade instructions.`
    }
  } else if (manager?.controllerVerified && controller && gitOpsPath) {
    const objectName = `${controller.namespace ? `${controller.namespace}/` : ''}${controller.name}`
    if (onNavigateToGitOps) {
      detail = `Managed by ${controller.kind} ${objectName}. Open it to upgrade through GitOps.`
      onClick = () => onNavigateToGitOps(gitOpsPath)
    } else {
      detail = `Managed by ${controller.kind} ${objectName}. Open the in-cluster upgrade instructions and apply the change through GitOps.`
    }
  } else if (manager?.ownership === 'gitops') {
    detail = manager.controller
      ? `This installation appears to be managed by ${manager.controller}. Open the upgrade instructions and apply the change through its source of truth.`
      : 'This installation appears to be managed through GitOps. Open the upgrade instructions and apply the change through its source of truth.'
  }

  const ageDetail = updateAgeDetail(updateStatus)
  const accessibleLabel = `${displayVersion(latestVersion)} available${ageDetail ? `. ${ageDetail}` : ''} — ${detail}`
  const action = managerLoading ? (
    <span className={actionClassName}>
      <UpgradeLabel version={latestVersion} tier={updateStatus.tier} />
      <span className="sr-only">{detail}</span>
    </span>
  ) : onClick ? (
    <button
      type="button"
      className={actionClassName}
      onClick={onClick}
      aria-label={accessibleLabel}
    >
      <UpgradeLabel version={latestVersion} tier={updateStatus.tier} />
    </button>
  ) : (
    <a
      href={IN_CLUSTER_UPGRADE_URL}
      target="_blank"
      rel="noreferrer"
      className={actionClassName}
      aria-label={accessibleLabel}
    >
      <UpgradeLabel version={latestVersion} tier={updateStatus.tier} />
    </a>
  )

  return (
    <span className="inline-flex flex-wrap items-center gap-x-1">
      <span>Radar <span className="font-mono">{displayVersion(version.currentVersion)}</span></span>
      <span className="inline-flex items-center gap-1">
        <span aria-hidden>·</span>
        <Tooltip
          content={accessibleLabel}
          className="!whitespace-normal !max-w-sm"
        >
          {action}
        </Tooltip>
      </span>
    </span>
  )
}

function upgradeActionClassName(tier: VersionUpdateTier): string {
  if (tier === 'patch') return 'text-theme-text-tertiary hover:text-accent-text'
  if (tier === 'minor') return 'font-medium text-accent hover:text-accent-light'
  return 'font-medium text-warning-text hover:opacity-80'
}

function updateAgeDetail(status: ReturnType<typeof getVersionUpdateStatus>): string | undefined {
  if (status.majorVersionBehind) return 'A major Radar upgrade is available.'
  if (status.tier !== 'stale' || !status.minorVersionsBehind) return undefined
  return `This installation is ${status.minorVersionsBehind} minor releases behind.`
}

function UpgradeLabel({ version, tier }: { version: string; tier: VersionUpdateTier }) {
  return (
    <>
      <ArrowUpCircle
        className={`h-3.5 w-3.5 shrink-0 ${tier === 'patch' ? 'text-accent-text' : ''}`}
        aria-hidden
      />
      <span><span className="font-mono">{displayVersion(version)}</span> available</span>
    </>
  )
}
