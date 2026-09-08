// Compact human-readable formatters for chart values and timestamps.
// Unit-tier breakpoints chosen so the rendered text stays short enough to
// fit inside chart axis labels and tooltips at typical font sizes.

export function formatMetricValue(value: number, unit: string): string {
  if (value === 0) return '0'
  if (value < 0) return `-${formatMetricValue(-value, unit)}`

  switch (unit) {
    case 'cores': {
      if (value < 0.0001) return '< 0.1m'
      if (value < 0.001) return `${(value * 1000).toFixed(1)}m`
      if (value < 1) return `${(value * 1000).toFixed(0)}m`
      return `${value.toFixed(2)}`
    }
    case 'bytes': {
      if (value < 1) return '< 1 B'
      if (value < 1024) return `${value.toFixed(0)} B`
      if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
      if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MiB`
      return `${(value / (1024 * 1024 * 1024)).toFixed(2)} GiB`
    }
    case 'bytes/s': {
      if (value < 1) return '< 1 B/s'
      if (value < 1024) return `${value.toFixed(0)} B/s`
      if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB/s`
      if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MiB/s`
      return `${(value / (1024 * 1024 * 1024)).toFixed(2)} GiB/s`
    }
    case 'count':
    case 'restarts': {
      if (value < 10000) return value.toFixed(0)
      if (value < 1e6) return `${(value / 1e3).toFixed(1)}k`
      return `${(value / 1e6).toFixed(1)}M`
    }
    case 'seconds': {
      if (value < 0.001) return `${(value * 1_000_000).toFixed(0)}µs`
      if (value < 1) return `${(value * 1000).toFixed(value < 0.01 ? 2 : 0)}ms`
      if (value < 60) return `${value.toFixed(value < 10 ? 2 : 1)}s`
      if (value < 3600) return `${(value / 60).toFixed(1)}m`
      return `${(value / 3600).toFixed(1)}h`
    }
    default:
      if (value < 0.01) return value.toExponential(1)
      if (value < 1) return value.toFixed(3)
      if (value < 100) return value.toFixed(2)
      if (value < 10000) return value.toFixed(0)
      if (value < 1e6) return `${(value / 1e3).toFixed(1)}k`
      if (value < 1e9) return `${(value / 1e6).toFixed(1)}M`
      return `${(value / 1e9).toFixed(2)}G`
  }
}

export function formatTimestamp(unix: number): string {
  const d = new Date(unix * 1000)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}
