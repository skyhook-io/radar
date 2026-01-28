interface Segment {
  value: number
  color: string
}

interface HealthRingProps {
  segments: Segment[]
  size?: number
  strokeWidth?: number
  label?: string
}

export function HealthRing({ segments, size = 48, strokeWidth = 5, label }: HealthRingProps) {
  const total = segments.reduce((sum, s) => sum + s.value, 0)
  if (total === 0) {
    return (
      <svg width={size} height={size} viewBox="0 0 100 100" className="shrink-0">
        <circle cx="50" cy="50" r="40" fill="none" stroke="currentColor" strokeWidth={strokeWidth} className="text-theme-border" />
        {label && (
          <text x="50" y="50" textAnchor="middle" dominantBaseline="central" className="fill-theme-text-tertiary text-[22px] font-semibold">
            0
          </text>
        )}
      </svg>
    )
  }

  const radius = 40
  const circumference = 2 * Math.PI * radius
  let accumulatedOffset = 0

  // Rotate -90deg so arcs start at 12 o'clock
  const arcs = segments
    .filter(s => s.value > 0)
    .map((seg) => {
      const fraction = seg.value / total
      const dashLength = fraction * circumference
      const gapLength = circumference - dashLength
      const offset = -accumulatedOffset * circumference
      accumulatedOffset += fraction
      return { color: seg.color, dashLength, gapLength, offset }
    })

  return (
    <svg width={size} height={size} viewBox="0 0 100 100" className="shrink-0">
      {/* Background ring */}
      <circle cx="50" cy="50" r={radius} fill="none" stroke="currentColor" strokeWidth={strokeWidth} className="text-theme-border opacity-30" />
      {/* Segments */}
      {arcs.map((arc, i) => (
        <circle
          key={i}
          cx="50"
          cy="50"
          r={radius}
          fill="none"
          stroke={arc.color}
          strokeWidth={strokeWidth}
          strokeDasharray={`${arc.dashLength} ${arc.gapLength}`}
          strokeDashoffset={arc.offset}
          strokeLinecap="butt"
          transform="rotate(-90 50 50)"
        />
      ))}
      {/* Center label */}
      {label && (
        <text x="50" y="50" textAnchor="middle" dominantBaseline="central" className="fill-theme-text-primary text-[22px] font-semibold">
          {label}
        </text>
      )}
    </svg>
  )
}
