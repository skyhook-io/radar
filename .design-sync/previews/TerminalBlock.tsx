import { TerminalBlock, TerminalBlockLabel } from '@skyhook-io/k8s-ui'

const wrap = { width: 520 }

export function RawError() {
  return (
    <div style={wrap}>
      <TerminalBlock>
        {'Back-off restarting failed container checkout-api in pod checkout-api-7d9f8c-x2kqp_payments(3f1c9a2e-5b7d-4e21-9c0a-8d2f61b4e7a3)'}
      </TerminalBlock>
    </div>
  )
}

export function Labeled() {
  return (
    <div style={wrap}>
      <TerminalBlock label="Selected crash line">
        {'panic: runtime error: invalid memory address or nil pointer dereference\n[signal SIGSEGV: segmentation violation code=0x1 addr=0x18 pc=0x8a3f2c]\n\ngoroutine 42 [running]:\nmain.(*OrderHandler).Submit(0xc0001a2000)\n\t/app/internal/orders/handler.go:118 +0x2c'}
      </TerminalBlock>
    </div>
  )
}

export function WithFooter() {
  return (
    <div style={wrap}>
      <TerminalBlock
        label="Selected log excerpt · last 3 of 212 lines"
        footer={
          <>
            <TerminalBlockLabel divider>Earlier lines</TerminalBlockLabel>
            <pre className="overflow-x-auto whitespace-pre-wrap break-words px-3 pb-2.5 font-mono text-xs leading-relaxed text-[var(--terminal-text)]">
              {'2026-09-27T10:14:02Z INFO  connected to postgres at payments-db-rw:5432\n2026-09-27T10:14:03Z INFO  listening on :8080'}
            </pre>
          </>
        }
      >
        {'2026-09-27T10:15:41Z WARN  pool exhausted, waiting for connection (32/32 in use)\n2026-09-27T10:15:46Z ERROR checkout failed: context deadline exceeded\n2026-09-27T10:15:46Z ERROR POST /v1/orders 504 5003ms'}
      </TerminalBlock>
    </div>
  )
}
