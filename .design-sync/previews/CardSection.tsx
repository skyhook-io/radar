import { CardBody, CardSection, TerminalBlock } from '@skyhook-io/k8s-ui'
import { FileText, TriangleAlert, Wrench } from 'lucide-react'
import type { CSSProperties } from 'react'

const card: CSSProperties = {
  width: 480,
  maxWidth: '100%',
  display: 'flex',
  flexDirection: 'column',
  gap: 16,
  padding: 16,
  borderRadius: 10,
  border: '1px solid var(--border-default, #D8E0EE)',
  background: 'var(--bg-surface, #F8FBFE)',
}

export function Tones() {
  return (
    <div style={card}>
      <CardSection icon={TriangleAlert} label="What's wrong" tone="warn">
        <CardBody>
          Pod checkout-api-7d9c4b8f6-x2kqz is in CrashLoopBackOff — the container has restarted 7 times in the last 4 minutes.
        </CardBody>
      </CardSection>
      <CardSection icon={Wrench} label="How to fix" tone="fix">
        <CardBody>
          Raise the memory limit above 512Mi, or fix the OOM in the order-serializer path. Last exit code was 137 (OOMKilled).
        </CardBody>
      </CardSection>
      <CardSection icon={FileText} label="Why it matters" tone="neutral" labelExtra="· blocks checkout">
        <CardBody>
          This Deployment backs the payments checkout flow. With 0/3 pods ready, live traffic is failing at the gateway.
        </CardBody>
      </CardSection>
    </div>
  )
}

export function WithTerminal() {
  return (
    <div style={card}>
      <CardSection icon={FileText} label="Raw error" tone="neutral" labelExtra="· retried 7×">
        <TerminalBlock label="last state · terminated">
          {`Back-off restarting failed container checkout-api in pod
checkout-api-7d9c4b8f6-x2kqz_payments(8f2c1a4e-6b0d-4e9a)
Exit Code: 137 · Reason: OOMKilled`}
        </TerminalBlock>
      </CardSection>
    </div>
  )
}
