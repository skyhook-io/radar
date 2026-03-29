import { Play, FileText, Key, Lock } from 'lucide-react'
import { Section, PropertyList, Property } from '../../ui/drawer-components'

interface WorkflowTemplateRendererProps {
  data: any
}

function getTemplateType(template: any): string {
  if (template.steps) return 'steps'
  if (template.dag) return 'dag'
  if (template.script) return 'script'
  if (template.container) return 'container'
  if (template.resource) return 'resource'
  return 'unknown'
}

function getTemplateImage(template: any): string | null {
  if (template.container?.image) return template.container.image
  if (template.script?.image) return template.script.image
  return null
}

export function WorkflowTemplateRenderer({ data }: WorkflowTemplateRendererProps) {
  const spec = data.spec || {}
  const templates = spec.templates || []
  const parameters = spec.arguments?.parameters || []
  const imagePullSecrets = spec.imagePullSecrets || []

  return (
    <>
      {/* Overview section */}
      <Section title="Overview" icon={Play}>
        <PropertyList>
          <Property label="Entrypoint" value={spec.entrypoint} />
          <Property label="Templates" value={templates.length} />
          <Property label="Service Account" value={spec.serviceAccountName} />
        </PropertyList>
      </Section>

      {/* Templates section */}
      {templates.length > 0 && (
        <Section title={`Templates (${templates.length})`} icon={FileText} defaultExpanded>
          <div className="space-y-1.5">
            {templates.map((template: any) => {
              const type = getTemplateType(template)
              const image = getTemplateImage(template)
              return (
                <div key={template.name} className="card-inner px-3 py-2 text-sm">
                  <div className="font-medium text-theme-text-primary">{template.name}</div>
                  <div className="text-xs text-theme-text-secondary mt-0.5">{type}</div>
                  {image && (
                    <div className="text-xs text-theme-text-tertiary mt-0.5 truncate" title={image}>
                      {image}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </Section>
      )}

      {/* Arguments section */}
      {parameters.length > 0 && (
        <Section title={`Arguments (${parameters.length})`} icon={Key} defaultExpanded={parameters.length <= 5}>
          <PropertyList>
            {parameters.map((param: any) => (
              <Property key={param.name} label={param.name} value={param.value} />
            ))}
          </PropertyList>
        </Section>
      )}

      {/* Image Pull Secrets section */}
      {imagePullSecrets.length > 0 && (
        <Section title={`Image Pull Secrets (${imagePullSecrets.length})`} icon={Lock}>
          <div className="flex flex-wrap gap-1">
            {imagePullSecrets.map((secret: any) => (
              <span
                key={secret.name}
                className="badge bg-theme-elevated text-theme-text-secondary"
              >
                {secret.name}
              </span>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
