import { useState, useMemo } from 'react'
import { Package, ChevronDown, ChevronRight, Search } from 'lucide-react'
import { Section, PropertyList, Property } from '../../ui/drawer-components'
import { Input } from '../../ui/Input'
import { formatAge } from '../resource-utils'
import { formatTrivyImage } from './trivy-shared'

interface SbomReportRendererProps {
  data: any
}

const INITIAL_SHOW_COUNT = 100

export function SbomReportRenderer({ data }: SbomReportRendererProps) {
  const [showAll, setShowAll] = useState(false)
  const [expanded, setExpanded] = useState(true)
  const [searchTerm, setSearchTerm] = useState('')

  const report = data.report || {}
  const scanner = report.scanner || {}
  const labels = data.metadata?.labels || {}
  const components = report.components || {}
  const bom = components.components || []
  const summary = report.summary || {}

  const containerName = labels['trivy-operator.container.name'] || '-'
  const image = formatTrivyImage(report)

  const componentsCount = summary.componentsCount || bom.length
  const depsCount = summary.dependenciesCount || 0
  const bomFormat = components.bomFormat || '-'
  const specVersion = components.specVersion || '-'

  const filteredBom = useMemo(() => {
    if (!searchTerm) return bom
    const term = searchTerm.toLowerCase()
    return bom.filter((comp: any) =>
      (comp.name || '').toLowerCase().includes(term) ||
      (comp.purl || '').toLowerCase().includes(term) ||
      (comp.type || '').toLowerCase().includes(term)
    )
  }, [bom, searchTerm])

  const hasActiveFilter = searchTerm !== ''
  const displayedComponents = showAll ? filteredBom : filteredBom.slice(0, INITIAL_SHOW_COUNT)

  return (
    <>
      {/* Report Overview */}
      <Section title="Report Overview" icon={Package}>
        <PropertyList>
          {containerName !== '-' && <Property label="Container" value={containerName} />}
          <Property label="Image" value={image} />
          <Property label="Format" value={`${bomFormat} ${specVersion}`} />
          <Property label="Components" value={String(componentsCount)} />
          {depsCount > 0 && <Property label="Dependencies" value={String(depsCount)} />}
          <Property label="Scanner" value={scanner.name ? `${scanner.name} ${scanner.version || ''}`.trim() : '-'} />
          <Property label="Scanned" value={report.updateTimestamp ? formatAge(report.updateTimestamp) + ' ago' : '-'} />
        </PropertyList>
      </Section>

      {/* Components Table */}
      {bom.length > 0 && (
        <Section title="Components">
          <button
            onClick={() => setExpanded(!expanded)}
            className="flex items-center gap-1 text-xs text-theme-text-secondary hover:text-theme-text-primary mb-2"
          >
            {expanded ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
            {bom.length} components
          </button>
          {expanded && (
            <div className="overflow-x-auto -mx-1">
              {/* Search */}
              <div className="flex items-center gap-2 mb-2 px-1">
                <div className="relative flex-1">
                  <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-tertiary" />
                  <Input
                    placeholder="Filter by name, type, or purl..."
                    value={searchTerm}
                    onChange={(e) => { setSearchTerm(e.target.value); setShowAll(false) }}
                    className="w-full pl-6 pr-2 py-1 text-xs bg-theme-bg border border-theme-border rounded text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
                  />
                </div>
              </div>
              {hasActiveFilter && (
                <div className="flex items-center gap-1 mb-2 px-1 text-xs text-theme-text-tertiary">
                  Showing {filteredBom.length} of {bom.length}
                  <button
                    onClick={() => { setSearchTerm(''); setShowAll(false) }}
                    className="ml-1 text-blue-400 hover:text-blue-300 hover:underline"
                  >
                    Clear
                  </button>
                </div>
              )}
              <table className="w-full text-xs">
                <thead>
                  <tr className="border-b border-theme-border text-theme-text-tertiary">
                    <th className="text-left py-1.5 px-1 font-medium">Name</th>
                    <th className="text-left py-1.5 px-1 font-medium">Version</th>
                    <th className="text-left py-1.5 px-1 font-medium">Type</th>
                    <th className="text-left py-1.5 px-1 font-medium">License</th>
                  </tr>
                </thead>
                <tbody>
                  {displayedComponents.map((comp: any, i: number) => {
                    const license = comp.licenses?.[0]?.license?.id || comp.licenses?.[0]?.license?.name || '-'
                    return (
                      <tr key={`${comp.name}-${comp.version}-${i}`} className="border-b border-theme-border/50 hover:bg-theme-hover/50">
                        <td className="py-1.5 px-1 text-theme-text-secondary max-w-[200px] truncate" title={comp.purl || comp.name}>{comp.name || '-'}</td>
                        <td className="py-1.5 px-1 text-theme-text-secondary font-mono">{comp.version || '-'}</td>
                        <td className="py-1.5 px-1 text-theme-text-tertiary">{comp.type || '-'}</td>
                        <td className="py-1.5 px-1 text-theme-text-tertiary max-w-[120px] truncate" title={license}>{license}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
              {displayedComponents.length === 0 && (
                <div className="py-4 text-center text-xs text-theme-text-tertiary">
                  No components match the current filter.
                </div>
              )}
              {!showAll && filteredBom.length > INITIAL_SHOW_COUNT && (
                <button
                  onClick={() => setShowAll(true)}
                  className="mt-2 text-xs text-blue-400 hover:text-blue-300 hover:underline"
                >
                  Show all {filteredBom.length} components
                </button>
              )}
            </div>
          )}
        </Section>
      )}
    </>
  )
}
