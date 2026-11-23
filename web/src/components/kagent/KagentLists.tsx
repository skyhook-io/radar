import { SectionTable, Sub, type TableColumn } from '../workspace/layout'
import { coverageEmpty } from '../workspace/coverage'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import type { KagentAgent, KagentMCPServer, KagentModel, KagentRef, KagentWorkspace } from '../../api/kagent'
import {
  changeText,
  consumersText,
  CredentialText,
  KagentName,
  Reading,
  refKey,
  revisionText,
  roleText,
  SessionValue,
  type KagentRuntime,
} from './shared'

export function KagentAgentsTable({
  data,
  runtime,
  onOpen,
}: {
  data: KagentWorkspace
  runtime: KagentRuntime
  onOpen: (ref: KagentRef) => void
}) {
  const wide = useMediaQuery('(min-width: 1280px)')
  const sessionByAgent = new Map(runtime.rows.map((row) => [`${row.agent.namespace}/${row.agent.name}`, row]))
  const session = (a: KagentAgent) => <SessionValue row={sessionByAgent.get(`${a.ref.namespace}/${a.ref.name}`)} runtime={runtime} />
  const change = (a: KagentAgent) => <Reading reading={a.reading} text={changeText(a.latestChange.state)} />
  const revision = (a: KagentAgent) => <span className="font-mono text-xs">{revisionText(a.newSessionsRevision)}</span>
  const model = (a: KagentAgent) =>
    a.model ? (
      <>
        <KagentName object={a.model.ref} onOpen={onOpen} />
        <Sub>
          {a.model.reading
            ? modelStatus(a.model.reading)
            : a.model.state === 'unread'
              ? `Unread · ${a.model.unread}`
              : a.model.state === 'notPermitted'
                ? 'Not permitted'
                : a.model.state === 'absent'
                  ? 'Absent'
                  : 'Present'}
        </Sub>
      </>
    ) : (
      <span className="text-theme-text-tertiary">{a.incomplete ? 'Not fully read' : 'No model declared'}</span>
    )
  const servers = (a: KagentAgent) => (
    <>
      <span>
        {a.incomplete ? '≥' : ''}
        {a.mcpServers.declared} declared{a.mcpServers.failing > 0 ? ` · ${a.mcpServers.failing} failing` : ''}
      </span>
      {a.mcpServers.unread > 0 && <Sub>{a.mcpServers.unread} unread</Sub>}
      {a.mcpServers.absent > 0 && <Sub>{a.mcpServers.absent} absent</Sub>}
      {a.mcpServers.notPermitted > 0 && <Sub>{a.mcpServers.notPermitted} not permitted</Sub>}
    </>
  )
  const columns: TableColumn<KagentAgent>[] = [
    {
      header: 'Agent',
      width: wide ? '20%' : '38%',
      cell: (a) => (
        <>
          <KagentName object={a.ref} onOpen={onOpen} />
          {a.description && <Sub>{a.description}</Sub>}
          {!wide && (
            <>
              <Sub>{a.ref.namespace}</Sub>
              <div className="mt-2">{change(a)}</div>
              <Sub>New sessions: {revision(a)}</Sub>
              <Sub>{session(a)}</Sub>
            </>
          )}
        </>
      ),
    },
    ...(wide
      ? [
          { header: 'Latest change', width: '14%', cell: change },
          { header: 'New sessions run', width: '14%', cell: revision },
        ]
      : []),
    { header: 'Model', width: wide ? '17%' : '34%', cell: model },
    { header: 'MCP servers', width: wide ? '12%' : '28%', cell: servers },
    ...(wide
      ? [
          { header: 'Sessions', width: '13%', cell: session },
          { header: 'Namespace', width: '10%', cell: (a: KagentAgent) => <span className="break-words text-xs">{a.ref.namespace}</span> },
        ]
      : []),
  ]
  return (
    <SectionTable
      title="Agents"
      subtitle="The latest change and the revision new sessions run are separate facts."
      columns={columns}
      rows={data.agents}
      rowKey={(a) => refKey(a.ref)}
      minWidth={0}
      rowResource={(a) => a.ref}
      onInspect={(r) => onOpen({ ...r, group: 'api.kagent.dev' })}
      empty={coverageEmpty(data.coverage.agents, 'Agents')}
    />
  )
}
export function mcpStatus(server: KagentMCPServer) {
  if (server.reading.state === 'stale') return server.failing ? 'Not processed · last discovery failed' : 'Latest change not processed'
  return server.failing
    ? 'Controller discovery failing'
    : server.reading.state === 'ready'
      ? 'Controller discovery OK'
      : server.reading.state === 'discoveryOff'
        ? 'Controller discovery disabled'
        : server.reading.state === 'stale'
          ? 'Latest change not processed'
          : 'Controller discovery not reported'
}
export function modelStatus(reading: Pick<KagentModel['reading'], 'state' | 'reason' | 'condition' | 'text'>) {
  if (reading.state === 'stale') return 'Latest change not processed'
  if (reading.state === 'ready') return 'Resolved'
  if (reading.reason === 'APIKeySecretNotFound') return 'API key Secret missing'
  if (reading.reason === 'APIKeySecretKeyNotFound') return 'API key Secret key missing'
  if (reading.condition === 'Accepted' && reading.state === 'failed') return 'Not accepted'
  return reading.state === 'notReported' ? 'Not reported' : reading.text
}
export function KagentMCPServers({ data, onOpen }: { data: KagentWorkspace; onOpen: (ref: KagentRef) => void }) {
  const wide = useMediaQuery('(min-width: 1280px)')
  return (
    <SectionTable
      title="MCP servers"
      subtitle="Controller discovery is separate from what a running session can reach."
      rows={data.mcpServers}
      rowKey={(s) => refKey(s.ref)}
      rowResource={(s) => s.ref}
      onInspect={(r) => onOpen({ ...r, group: 'api.kagent.dev' })}
      minWidth={0}
      empty={coverageEmpty(data.coverage.mcpServers, 'MCP servers')}
      columns={[
        {
          header: 'MCP server',
          width: wide ? '20%' : '27%',
          cell: (s) => (
            <>
              <KagentName object={s.ref} onOpen={onOpen} />
              {!wide && <Sub>{s.ref.namespace}</Sub>}
            </>
          ),
        },
        {
          header: 'Status',
          width: wide ? '22%' : '27%',
          cell: (s) => (
            <>
              <Reading reading={s.reading} text={mcpStatus(s)} />
              {!wide && <Sub>Used by: {consumersText(s.usedBy)}</Sub>}
            </>
          ),
        },
        {
          header: 'Tools offered',
          width: wide ? '10%' : '23%',
          cell: (s) => (s.toolsOfferedKnown ? s.toolsOffered.length : <span className="text-theme-text-tertiary">Unknown</span>),
        },
        ...(wide ? [{ header: 'Used by', width: '15%', cell: (s: KagentMCPServer) => consumersText(s.usedBy) }] : []),
        {
          header: wide ? 'Asked-for tools not offered' : 'Tools missing',
          width: wide ? '21%' : '23%',
          cell: (s) => (s.toolsOfferedKnown ? s.askedNotOffered?.map((t) => t.tool).join(', ') || 'None seen' : 'Unknown'),
        },
        ...(wide
          ? [{ header: 'Namespace', width: '12%', cell: (s: KagentMCPServer) => <span className="break-words">{s.ref.namespace}</span> }]
          : []),
      ]}
    />
  )
}
export function KagentModels({ data, onOpen }: { data: KagentWorkspace; onOpen: (ref: KagentRef) => void }) {
  const wide = useMediaQuery('(min-width: 1280px)')
  return (
    <div className="space-y-5">
      <SectionTable
        title="Models"
        subtitle="Credential references name Secrets and keys; Radar never reads their values."
        rows={data.models}
        rowKey={(m) => refKey(m.ref)}
        rowResource={(m) => m.ref}
        onInspect={(r) => onOpen({ ...r, group: 'api.kagent.dev' })}
        minWidth={0}
        empty={coverageEmpty(data.coverage.models, 'models')}
        columns={[
          {
            header: 'ModelConfig',
            width: wide ? '20%' : '38%',
            cell: (m) => (
              <>
                <KagentName object={m.ref} onOpen={onOpen} />
                {!wide && (
                  <>
                    <Sub>{m.ref.namespace}</Sub>
                    <Sub>
                      {m.provider || 'Provider not reported'} · {m.model || 'Model not reported'}
                    </Sub>
                    <Sub>Used by: {consumersText(m.usedBy)}</Sub>
                  </>
                )}
              </>
            ),
          },
          { header: 'Status', width: wide ? '18%' : '27%', cell: (m) => <Reading reading={m.reading} text={modelStatus(m.reading)} /> },
          ...(wide
            ? [
                {
                  header: 'Provider / Model',
                  width: '17%',
                  cell: (m: KagentModel) => (
                    <>
                      {m.provider || 'Not reported'}
                      <Sub>{m.model || 'Not reported'}</Sub>
                    </>
                  ),
                },
              ]
            : []),
          { header: 'Credential reference', width: wide ? '20%' : '35%', cell: (m) => <CredentialText credential={m.credential} /> },
          ...(wide
            ? [
                {
                  header: 'Used by',
                  width: '15%',
                  cell: (m: KagentModel) => (
                    <>
                      {consumersText(m.usedBy)}
                      <Sub>
                        {Object.entries(m.usedBy.byRole ?? {})
                          .map(([r, n]) => `${roleText(r)}: ${n}`)
                          .join(' · ')}
                      </Sub>
                    </>
                  ),
                },
                { header: 'Namespace', width: '10%', cell: (m: KagentModel) => <span className="break-words">{m.ref.namespace}</span> },
              ]
            : []),
        ]}
      />
      <SectionTable
        title="Model providers"
        subtitle="kagent 1.0.1 does not report ModelProviderConfig status."
        rows={data.modelProviders}
        rowKey={(p) => refKey(p.ref)}
        rowResource={(p) => p.ref}
        onInspect={(r) => onOpen({ ...r, group: 'api.kagent.dev' })}
        minWidth={0}
        empty={coverageEmpty(data.coverage.modelProviders, 'ModelProviderConfigs')}
        columns={[
          { header: 'ModelProviderConfig', cell: (p) => p.ref.name },
          { header: 'Provider', cell: (p) => p.provider || 'Not reported' },
          { header: 'Status', cell: (p) => (p.reading.state === 'notReported' ? 'Not reported' : p.reading.text) },
          { header: 'Namespace', cell: (p) => p.ref.namespace },
        ]}
      />
    </div>
  )
}
