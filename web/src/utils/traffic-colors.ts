// Shared traffic visualization palette
// Used by TrafficGraph and TrafficFilterSidebar

// Namespace color palette — maximally distinct colors spread across the hue spectrum
export const NAMESPACE_PALETTE = [
  '#dc2626', // red-600
  '#2563eb', // blue-600
  '#16a34a', // green-600
  '#9333ea', // purple-600
  '#ea580c', // orange-600
  '#0891b2', // cyan-600
  '#c026d3', // fuchsia-600
  '#65a30d', // lime-600
  '#0d9488', // teal-600
  '#e11d48', // rose-600
  '#7c3aed', // violet-600
  '#ca8a04', // yellow-600
  '#4f46e5', // indigo-600
  '#db2777', // pink-600
  '#059669', // emerald-600
  '#d97706', // amber-600
]

// Named colors for common environments
export const NAMESPACE_NAMED_COLORS: Record<string, string> = {
  production: '#991b1b',  // Red-800
  prod: '#991b1b',
  staging: '#854d0e',     // Yellow-800
  stg: '#854d0e',
  dev: '#1e40af',         // Blue-800
  development: '#1e40af',
  default: '#374151',     // Gray-700
}

// Shared color assignment state — ensures graph and sidebar always agree
const assignedColors = new Map<string, string>()
let colorIndex = 0

export function getNamespaceColor(namespace: string | undefined): string {
  if (!namespace) return '#44403c' // Stone-700 for external
  const lower = namespace.toLowerCase()

  if (NAMESPACE_NAMED_COLORS[lower]) return NAMESPACE_NAMED_COLORS[lower]

  if (assignedColors.has(namespace)) {
    return assignedColors.get(namespace)!
  }

  // Assign next color in sequence (avoids hash collisions)
  const color = NAMESPACE_PALETTE[colorIndex % NAMESPACE_PALETTE.length]
  assignedColors.set(namespace, color)
  colorIndex++
  return color
}
