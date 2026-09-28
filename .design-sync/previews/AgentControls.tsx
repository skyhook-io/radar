import { useState } from 'react'
import { AgentControls } from '@skyhook-io/k8s-ui'

const wrap = { width: 380, padding: 12 }

const claude = {
  name: 'claude', label: 'Claude Code', path: '/opt/homebrew/bin/claude', version: '2.1.4',
  present: true, supported: true, profiles: ['safeguarded', 'full-local'] as const,
}
const codex = {
  name: 'codex', label: 'Codex', path: '/usr/local/bin/codex', version: '0.46.0',
  present: true, supported: true, profiles: ['safeguarded', 'full-local'] as const,
}
const cursor = {
  name: 'cursor-agent', label: 'Cursor', path: '/usr/local/bin/cursor-agent', version: '2025.10.2',
  present: true, supported: true, profiles: ['full-local'] as const,
}
const agents = [claude, codex, cursor].map((a) => ({ ...a, profiles: [...a.profiles] }))

function Controls({ agent, profile, model = '', effort = '' }: { agent: string; profile: 'safeguarded' | 'full-local'; model?: string; effort?: string }) {
  const [a, setA] = useState(agent)
  const [p, setP] = useState(profile)
  const [m, setM] = useState(model)
  const [e, setE] = useState(effort)
  return (
    <div style={wrap}>
      <AgentControls
        agents={agents}
        selectedAgent={a}
        onSelectAgent={(v) => { setA(v); setM(''); setE('') }}
        profile={p}
        onSetProfile={setP}
        model={m}
        onSetModel={setM}
        effort={e}
        onSetEffort={setE}
      />
    </div>
  )
}

export function ClaudeSafeguarded() {
  return <Controls agent="claude" profile="safeguarded" model="sonnet" />
}

export function CodexFullLocal() {
  return <Controls agent="codex" profile="full-local" model="gpt-5-codex" effort="high" />
}

export function CursorOnlyFullLocal() {
  return <Controls agent="cursor-agent" profile="full-local" />
}
