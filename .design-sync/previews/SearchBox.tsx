import { SearchBox } from '@skyhook-io/k8s-ui'

const stack = { display: 'flex', flexDirection: 'column', gap: 14, width: 340 } as const
const noop = () => {}

export function Empty() {
  return (
    <div style={stack}>
      <SearchBox value="" onChange={noop} scope="resources" shortcutId="preview-resources-search" />
    </div>
  )
}

export function WithQuery() {
  return (
    <div style={stack}>
      <SearchBox value="payment-service" onChange={noop} scope="resources" shortcutId="preview-query-search" />
    </div>
  )
}

export function CustomPlaceholder() {
  return (
    <div style={stack}>
      <SearchBox
        value=""
        onChange={noop}
        scope="timeline"
        shortcutId="preview-timeline-search"
        placeholder="Filter events by reason or object..."
      />
    </div>
  )
}
