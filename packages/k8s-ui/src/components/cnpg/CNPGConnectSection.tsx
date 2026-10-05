import { useState } from 'react'
import { Check, Copy } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import { CNPG_GROUP } from '../resources/resource-utils-cnpg'
import { CNPG_DEFAULT_PORT, cnpgConnectionURI, cnpgConnectInfo, cnpgPsqlCommand, cnpgPortForwardCommand, type CNPGConnectEndpoint } from './connect'
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
    <div className="flex items-center gap-1 rounded-md bg-theme-elevated px-2 py-1">
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
  onOpenReachability,
  showHeading = true,
}: {
  cluster: any
  poolers?: any[]
  /** False when Poolers could not be listed, so a Pooler may exist that is not shown. */
  poolersKnown?: boolean
  onNavigate?: NavigateToRef
  /** Opens a host's Service on its Reachability tab; the link shows only when given. */
  onOpenReachability?: (service: { namespace: string; name: string }) => void
  /** False where the host already titles it (e.g. the Connect dialog). */
  showHeading?: boolean
}) {
  const info = cnpgConnectInfo(cluster, poolers)
  const ns: string = cluster?.metadata?.namespace ?? ''
  const primary = info.endpoints[0]
  const local = { ...primary, host: '127.0.0.1', port: CNPG_DEFAULT_PORT }
  return (
    <>
      {showHeading && <SectionHeading hint="from the Cluster spec · hosts resolve inside the Kubernetes cluster">Connect</SectionHeading>}
      <FactGrid>
        <FactRow label="Services">
          <ul className="grid grid-cols-[5rem_minmax(0,1fr)_auto] items-baseline gap-x-2 gap-y-2">
            {info.endpoints.map((ep) => (
              <li key={`${ep.role}/${ep.name}`} className="contents">
                <span className="w-20 shrink-0 text-xs text-theme-text-tertiary">{ROLE_LABEL[ep.role]}</span>
                <div className="min-w-0 text-xs [overflow-wrap:anywhere]">
                  <RefLink refTo={{ kind: ep.role === 'pooler' ? 'Pooler' : 'Service', group: ep.role === 'pooler' ? CNPG_GROUP : '', namespace: ns, name: ep.name }} onNavigate={onNavigate} mono>
                    {`${ep.host}:${ep.port}`}
                  </RefLink>
                  <div className="mt-0.5 text-xs text-theme-text-secondary">{ep.selects}</div>
                  {ep.portFromTemplate && <div className="text-xs text-theme-text-tertiary">port from serviceTemplate</div>}
                </div>
                {onOpenReachability ? (
                  <Tooltip
                    content="Radar's check of this Service's path: endpoints, ready Pods and the NetworkPolicy rules in the way. It does not log in to PostgreSQL."
                    position="top"
                  >
                    <button type="button" onClick={() => onOpenReachability({ namespace: ns, name: ep.name })} className="text-xs text-accent-text hover:underline">
                      Reachability →
                    </button>
                  </Tooltip>
                ) : <span />}
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
          {info.database.value ? <span className="font-mono text-xs">{info.database.value}</span> : <span className="text-theme-text-tertiary">Unknown</span>}
          <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">{info.database.source}</div>
        </FactRow>
        <FactRow label="Owner">
          {info.owner.value ? <span className="font-mono text-xs">{info.owner.value}</span> : <span className="text-theme-text-tertiary">Unknown</span>}
          <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">{info.owner.source}</div>
        </FactRow>
        <FactRow label="Credentials">
          <span className="text-xs">
            <RefLink refTo={{ kind: 'Secret', group: '', namespace: ns, name: info.secret.name }} onNavigate={onNavigate} mono>
              {info.secret.name}
            </RefLink>
          </span>
          <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
            {info.secret.source} · Radar does not read it; the password is its <span className="font-mono">password</span> key
          </div>
        </FactRow>
        {info.database.value && (
          <FactRow label="Inside Kubernetes">
            <div className="space-y-1">
              <Snippet text={cnpgConnectionURI(primary, info)} label="connection string" />
              <Snippet text={cnpgPsqlCommand(primary, info)} label="psql command" />
            </div>
            <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
              Via <span className="font-mono">{primary.name}</span>. Replace <span className="font-mono">{'<password>'}</span>; psql prompts for it.
            </div>
          </FactRow>
        )}
        {info.database.value && (
          <FactRow label="From this computer">
            <div className="space-y-1">
              <Snippet text={cnpgPortForwardCommand(primary, ns)} label="port-forward command" />
              <Snippet text={cnpgConnectionURI(local, info)} label="local connection string" />
              <Snippet text={cnpgPsqlCommand(local, info)} label="local psql command" />
            </div>
            <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">Keep port-forward running, then connect in another terminal. Local port 5432 must be free. Replace {'<password>'}; psql prompts for it.</div>
          </FactRow>
        )}
      </FactGrid>
    </>
  )
}
