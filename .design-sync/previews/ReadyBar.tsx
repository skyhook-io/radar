import { ReadyBar } from '@skyhook-io/k8s-ui'

const col = { display: 'flex', flexDirection: 'column', gap: 10, padding: 8 } as const
const row = { display: 'flex', gap: 20, alignItems: 'center', padding: 8 }

export function ReadyStates() {
  return (
    <div style={col}>
      <ReadyBar ready={6} desired={6} />
      <ReadyBar ready={4} desired={5} />
      <ReadyBar ready={1} desired={3} />
      <ReadyBar ready={0} desired={4} />
      <ReadyBar ready={0} desired={0} />
    </div>
  )
}

export function Widths() {
  return (
    <div style={row}>
      <ReadyBar ready={3} desired={5} width="w-12" />
      <ReadyBar ready={3} desired={5} width="w-24" />
      <ReadyBar ready={3} desired={5} width="w-32" />
    </div>
  )
}
