import { useState } from 'react'
import { Check, Copy } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import { CNPG_GROUP } from '../resources/resource-utils-cnpg'
import { cnpgConnectionURI, cnpgConnectInfo, cnpgPsqlCommand, type CNPGConnectEndpoint } from './connect'
import { type NavigateToRef, RefLink } from '../ui/RefLink'
import { FactGrid, FactRow } from '../facts'
import { SectionHeading } from '../ui/FoldSection'

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    navigator.clipboard?.writeText(text).then(
      () => {
        setCopied(true)
        setTimeout(() => setCopied(false), 2000)
      },
      () => {},
    )
  }
  return (
    <Tooltip content={copied ? 'Copied' : `Copy ${label}`} position="top">
      <button
        type="button"
        onClick={copy}
        aria-label={`Copy ${label}`}
        className="rounded p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
      >
        {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
      </button>
    </Tooltip>
  )
}

function Snippet({ text, label }: { text: string; label: string }) {
  return (
    <div className="flex items-start gap-1 rounded-md bg-theme-elevated px-2 py-1">
      <code className="min-w-0 flex-1 break-all font-mono text-xs text-theme-text-primary">{text}</code>
      <CopyButton text={text} label={label} />
    </div>
  )
}

const ROLE_LABEL: Record<CNPGConnectEndpoint['role'], string> = {
  rw: 'Read-write',
  ro: 'Read-only',
  r: 'Any instance',
  pooler: 'Pooler',
  additional: 'Additional',
}

/**
 * How applications reach the cluster: Services, application database and
 * owner, and the credentials Secret by name. Never reads the Secret.
 */

/**
 * Selects the Connect heading inside one summary. A data attribute, not an
 * id: a drawer summary can sit over a page summary of the same kind, so a host
 * scrolls to it within its own summary's element.
 */
export function CNPGConnectSection({
  cluster,
  poolers,
  poolersKnown,
  onNavigate,
  showHeading = true,
}: {
  cluster: any
  poolers?: any[]
  /** False when Poolers could not be listed, so a Pooler may exist that is not shown. */
  poolersKnown?: boolean
  onNavigate?: NavigateToRef
  /** False where the host already titles it (e.g. the Connect dialog). */
  showHeading?: boolean
}) {
  const info = cnpgConnectInfo(cluster, poolers)
  const ns: string = cluster?.metadata?.namespace ?? ''
  const primary = info.endpoints[0]
  return (
    <>
      {showHeading && <SectionHeading hint="from the Cluster spec · hosts resolve inside the Kubernetes cluster">Connect</SectionHeading>}
      <FactGrid>
        <FactRow label="Services">
          <ul className="space-y-1">
            {info.endpoints.map((ep) => (
              <li key={`${ep.role}/${ep.name}`} className="flex flex-wrap items-baseline gap-x-2">
                <span className="w-20 shrink-0 text-xs text-theme-text-tertiary">{ROLE_LABEL[ep.role]}</span>
                <RefLink refTo={{ kind: ep.role === 'pooler' ? 'Pooler' : 'Service', group: ep.role === 'pooler' ? CNPG_GROUP : '', namespace: ns, name: ep.name }} onNavigate={onNavigate} mono>
                  {`${ep.host}:${ep.port}`}
                </RefLink>
                <span className="text-xs text-theme-text-secondary">{ep.selects}</span>
                {ep.portFromTemplate && <span className="text-xs text-theme-text-tertiary">port from serviceTemplate</span>}
              </li>
            ))}
          </ul>
          {info.disabled.length > 0 && (
            <div className="mt-1 text-[11.5px] text-theme-text-tertiary">
              Disabled in spec.managed.services: {info.disabled.map((t) => `${cluster?.metadata?.name}-${t}`).join(', ')}
            </div>
          )}
          {poolersKnown === false && <div className="mt-1 text-[11.5px] text-theme-text-tertiary">No access to Poolers: one may also front this cluster.</div>}
          {info.replicaCluster && (
            <div className="mt-1 text-[11.5px] text-theme-text-tertiary">A replica cluster: every Service reaches instances that only replay until it is promoted.</div>
          )}
        </FactRow>
        <FactRow label="Database">
          {info.database.value ? <span className="font-mono">{info.database.value}</span> : <span className="text-theme-text-tertiary">Unknown</span>}
          <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">{info.database.source}</div>
        </FactRow>
        <FactRow label="Owner">
          {info.owner.value ? <span className="font-mono">{info.owner.value}</span> : <span className="text-theme-text-tertiary">Unknown</span>}
          <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">{info.owner.source}</div>
        </FactRow>
        <FactRow label="Credentials">
          <RefLink refTo={{ kind: 'Secret', group: '', namespace: ns, name: info.secret.name }} onNavigate={onNavigate} mono>
            {info.secret.name}
          </RefLink>
          <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
            {info.secret.source} · Radar does not read it; the password is its <span className="font-mono">password</span> key
          </div>
        </FactRow>
        {info.database.value && (
          <FactRow label="Templates">
            <div className="space-y-1">
              <Snippet text={cnpgConnectionURI(primary, info)} label="connection string" />
              <Snippet text={cnpgPsqlCommand(primary, info)} label="psql command" />
            </div>
            <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
              Via <span className="font-mono">{primary.name}</span>. Replace <span className="font-mono">{'<password>'}</span>; psql prompts for it. From outside the cluster, port-forward the Service first.
            </div>
          </FactRow>
        )}
      </FactGrid>
    </>
  )
}
