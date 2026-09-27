import { RowActionMenu } from '@skyhook-io/k8s-ui'
import { CirclePause, History, RefreshCw, RotateCw, Trash2 } from 'lucide-react'
import type { CSSProperties } from 'react'

const noop = () => {}

function appActions(name: string) {
  return [
    { key: 'sync', label: 'Sync', icon: RefreshCw, onClick: noop },
    { key: 'refresh', label: 'Refresh', icon: RotateCw, onClick: noop },
    { key: 'suspend', label: 'Suspend', icon: CirclePause, onClick: noop },
    { key: 'rollback', label: 'Rollback', icon: History, onClick: noop, divider: true },
    { key: 'delete', label: `Delete ${name}`, icon: Trash2, onClick: noop, danger: true },
  ]
}

const table: CSSProperties = {
  width: 440,
  maxWidth: '100%',
  border: '1px solid var(--border-default, #D8E0EE)',
  borderRadius: 8,
  background: 'var(--bg-surface, #F8FBFE)',
  overflow: 'visible',
}
const row: CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: 12,
  padding: '8px 12px',
  borderTop: '1px solid var(--border-default, #D8E0EE)',
}
const nameCell: CSSProperties = { flex: 1, minWidth: 0, fontSize: 13, fontWeight: 500, color: 'var(--text-primary, #1A1C20)' }
const sub: CSSProperties = { fontSize: 11, color: 'var(--text-secondary, #555860)' }
const dot = (bg: string): CSSProperties => ({ width: 8, height: 8, borderRadius: 999, background: bg, flexShrink: 0 })

export function InApplicationRows() {
  return (
    <div style={table}>
      <div style={{ ...row, borderTop: 'none' }}>
        <span style={dot('#10b981')} />
        <span style={nameCell}>guestbook</span>
        <span style={sub}>Synced · Healthy</span>
        <RowActionMenu items={appActions('guestbook')} ariaLabel="Actions for guestbook" />
      </div>
      <div style={row}>
        <span style={dot('#f59e0b')} />
        <span style={nameCell}>cart-service</span>
        <span style={sub}>OutOfSync · Progressing</span>
        <RowActionMenu items={appActions('cart-service')} ariaLabel="Actions for cart-service" />
      </div>
      <div style={row}>
        <span style={dot('#ef4444')} />
        <span style={nameCell}>payment-gateway</span>
        <span style={sub}>OutOfSync · Degraded</span>
        <RowActionMenu items={appActions('payment-gateway')} ariaLabel="Actions for payment-gateway" />
      </div>
    </div>
  )
}

export function CompactVsFull() {
  const items = [
    { key: 'sync', label: 'Sync', icon: RefreshCw, onClick: noop },
    { key: 'suspend', label: 'Suspend', icon: CirclePause, onClick: noop },
    { key: 'delete', label: 'Delete', icon: Trash2, onClick: noop, danger: true, divider: true },
  ]
  const cell: CSSProperties = { display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6 }
  return (
    <div style={{ display: 'flex', gap: 40, alignItems: 'flex-start' }}>
      <div style={cell}>
        <RowActionMenu items={items} compact />
        <span style={sub}>compact (table row)</span>
      </div>
      <div style={cell}>
        <RowActionMenu items={items} compact={false} />
        <span style={sub}>full (toolbar)</span>
      </div>
    </div>
  )
}
