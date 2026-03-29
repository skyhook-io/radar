import { useId } from 'react'

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
  const filterId = useId()
  const total = segments.reduce((sum, s) => sum + s.value, 0)

  const radius = 40
  const circumference = 2 * Math.PI * radius
  // 270° arc = 75% of circumference
  const arcLength = 0.75 * circumference
  const gapLength = circumference - arcLength

  if (total === 0) {
    return (
      <svg width={size} height={size} viewBox="0 0 100 100" className="shrink-0">
        {/* Background track (270° arc) */}
        <circle
          cx="50" cy="50" r={radius}
          fill="none" stroke="currentColor" strokeWidth={strokeWidth}
          strokeDasharray={`${arcLength} ${gapLength}`}
          strokeLinecap="round"
          transform="rotate(135 50 50)"
          className="text-theme-border"
        />
        {label && (
          <text x="50" y="48" textAnchor="middle" dominantBaseline="central" className="fill-theme-text-tertiary text-[22px] font-semibold font-mono">
            0
          </text>
        )}
      </svg>
    )
  }

  let accumulatedFraction = 0

  const visibleSegments = segments.filter(s => s.value > 0)
  const arcs = visibleSegments.map((seg, i) => {
    const fraction = seg.value / total
    // Scale to 270° arc (75% of circumference)
    const dashLength = fraction * arcLength
    // Offset within the 270° arc range
    const offset = -accumulatedFraction * arcLength
    const isFirst = i === 0
    const isLast = i === visibleSegments.length - 1
    accumulatedFraction += fraction
    return { color: seg.color, dashLength, offset, isFirst, isLast }
  })

  return (
    <svg width={size} height={size} viewBox="0 0 100 100" className="shrink-0">
      <defs>
        <filter id={`arcglow-${filterId}`}>
          <feGaussianBlur stdDeviation="2.5" result="blur"/>
          <feMerge>
            <feMergeNode in="blur"/>
            <feMergeNode in="SourceGraphic"/>
          </feMerge>
        </filter>
      </defs>
      {/* Background track (270° arc) */}
      <circle
        cx="50" cy="50" r={radius}
        fill="none" stroke="currentColor" strokeWidth={strokeWidth}
        strokeDasharray={`${arcLength} ${gapLength}`}
        strokeLinecap="round"
        transform="rotate(135 50 50)"
        className="text-theme-border opacity-30"
      />
      {/* Segments */}
      <g filter={`url(#arcglow-${filterId})`}>
        {arcs.map((arc, i) => (
          <circle
            key={i}
            cx="50"
            cy="50"
            r={radius}
            fill="none"
            stroke={arc.color}
            strokeWidth={strokeWidth}
            strokeDasharray={`${arc.dashLength} ${circumference - arc.dashLength}`}
            strokeDashoffset={arc.offset}
            strokeLinecap={arc.isFirst || arc.isLast ? 'round' : 'butt'}
            transform="rotate(135 50 50)"
          />
        ))}
      </g>
      {/* Center label — nudged up slightly for visual centering in 270° arc */}
      {label && (
        <text x="50" y="48" textAnchor="middle" dominantBaseline="central" className="fill-theme-text-primary text-[22px] font-semibold font-mono">
          {label}
        </text>
      )}
    </svg>
  )
}
