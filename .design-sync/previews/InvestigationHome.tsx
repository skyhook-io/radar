import { InvestigationHome } from '@skyhook-io/k8s-ui'

const frame = { width: 720, height: 360, display: 'flex' } as const

export function FirstRun() {
  return (
    <div style={frame}>
      <InvestigationHome agentLabel="Claude Code" onBrowseIssues={() => {}} />
    </div>
  )
}

export function WithHistory() {
  return (
    <div style={frame}>
      <InvestigationHome agentLabel="Codex" />
    </div>
  )
}
