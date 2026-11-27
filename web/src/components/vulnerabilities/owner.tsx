import { createContext, useContext, type ReactNode } from 'react'
import { Tooltip } from '@skyhook-io/k8s-ui/components/ui/Tooltip'
import { WithTooltip } from '../ui/Tooltip'
import {
  ownerLookup,
  type OwnerLookup,
  type VulnerabilitiesResponse,
  type VulnerabilityOwnerManager,
  type VulnerabilityWorkloadOwner,
} from '../../api/vulnerabilities'

/** The answer's owners, for any workload link on the page; absent from an older Radar. */
export interface OwnerInfo {
  find: OwnerLookup
  teamLabel: string
  unavailable?: string
}

const OwnerContext = createContext<OwnerInfo | undefined>(undefined)

export function ownerInfo(data: VulnerabilitiesResponse | undefined): OwnerInfo | undefined {
  if (!data?.ownership) return undefined
  return { find: ownerLookup(data.owners), teamLabel: data.ownership.teamLabel, unavailable: data.ownership.unavailable }
}

export function OwnerProvider({ info, children }: { info: OwnerInfo | undefined; children: ReactNode }) {
  return <OwnerContext.Provider value={info}>{children}</OwnerContext.Provider>
}

export function useOwners(): OwnerInfo | undefined {
  return useContext(OwnerContext)
}

const TOOL_LABEL: Record<VulnerabilityOwnerManager['tool'], string> = { helm: 'Helm release', argocd: 'Argo CD', fluxcd: 'Flux' }

export function managerLabel(m: VulnerabilityOwnerManager): string {
  if (m.tool === 'fluxcd') return m.kind === 'Kustomization' ? 'Flux Kustomization' : 'Flux HelmRelease'
  if (m.tool === 'argocd') return 'Argo CD Application'
  return TOOL_LABEL[m.tool]
}

/** A repository address without its scheme or .git, for a narrow cell. */
export function shortRepo(repo: string): string {
  return repo.replace(/^[a-z][a-z0-9+.-]*:\/\//i, '').replace(/^git@([^:]+):/, '$1/').replace(/\.git$/, '')
}

/** The owner's detail lines, for a tooltip. */
function OwnerDetail({ owner, teamLabel }: { owner: VulnerabilityWorkloadOwner; teamLabel: string }) {
  const m = owner.manager
  return (
    <span className="flex flex-col gap-1 text-left">
      {m && (
        <>
          <span>{managerLabel(m)} <span className="font-mono">{m.namespace}/{m.name}</span></span>
          {m.chart && <span>Chart <span className="font-mono">{m.chart}</span></span>}
          {(m.sources ?? []).map((s) => (
            <span key={`${s.repo}/${s.path ?? ''}`} className="break-all font-mono">{s.repo}{s.path ? ` (${s.path})` : ''}</span>
          ))}
          {m.tool === 'helm' && <span>Helm records no source repository.</span>}
          {m.shared && <span>Another Argo CD or Flux object lists it too; this is the one its own metadata names.</span>}
        </>
      )}
      {!m && owner.managerUnknown && <span>Radar couldn't tell what deploys it: the evidence disagrees or couldn't all be read.</span>}
      {owner.team && <span>Team <span className="font-mono">{owner.team}</span>, from the {owner.teamFrom === 'namespace' ? "namespace's" : "workload's"} <span className="font-mono">{teamLabel}</span> label</span>}
      {m && <span className="text-theme-text-tertiary">This is the app that deploys it. The image tag may be set by CI, an override or a values file.</span>}
    </span>
  )
}

/** The Owner column of By workload. Empty when Radar found nothing, never a guess. */
export function OwnerCell({ workload }: { workload: { kind: string; group?: string; namespace: string; name: string } }) {
  const info = useOwners()
  if (!info) return <span />
  if (info.unavailable) {
    return (
      <WithTooltip tip="Radar couldn't read owners for this answer: it is still loading the cluster or its connection changed. Try again shortly.">
        <span className="text-xs text-theme-text-tertiary">Unknown</span>
      </WithTooltip>
    )
  }
  const owner = info.find(workload)
  if (!owner) return <span />
  const m = owner.manager
  const repo = m?.sources?.[0]?.repo
  return (
    <Tooltip content={<OwnerDetail owner={owner} teamLabel={info.teamLabel} />}>
      <span className="flex min-w-0 flex-col text-xs">
        {m ? (
          <>
            <span className="truncate text-theme-text-primary">{m.name}</span>
            <span className="truncate text-theme-text-tertiary">
              {m.tool === 'helm' ? (m.chart ? `Helm · ${m.chart}` : 'Helm release') : repo ? `${TOOL_LABEL[m.tool]} · ${shortRepo(repo)}` : managerLabel(m)}
            </span>
          </>
        ) : owner.managerUnknown ? (
          <span className="text-theme-text-tertiary">Unknown</span>
        ) : null}
        {owner.team && <span className="truncate text-theme-text-secondary">team: {owner.team}</span>}
      </span>
    </Tooltip>
  )
}

/** One line for a workload link's tooltip, or undefined when nothing is known. */
export function ownerTip(owner: VulnerabilityWorkloadOwner | undefined): string | undefined {
  if (!owner) return undefined
  const parts: string[] = []
  const m = owner.manager
  if (m) {
    const from = (m.sources ?? []).map((s) => `${shortRepo(s.repo)}${s.path ? ` (${s.path})` : ''}`).join(', ')
    parts.push(`Deployed by ${managerLabel(m)} ${m.namespace}/${m.name}${m.chart ? `, chart ${m.chart}` : ''}${from ? `, from ${from}` : ''}`)
    if (m.shared) parts.push('Another Argo CD or Flux object lists it too')
  } else if (owner.managerUnknown) {
    parts.push("Radar couldn't tell what deploys it")
  }
  if (owner.team) parts.push(`Team ${owner.team}`)
  return parts.length ? `${parts.join('. ')}.` : undefined
}
