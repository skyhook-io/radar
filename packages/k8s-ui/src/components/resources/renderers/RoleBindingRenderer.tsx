import { Shield, Users } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ResourceLink, AlertBanner } from '../../ui/drawer-components'

interface RoleBindingRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function getSubjectKindBadgeClass(kind: string): string {
  switch (kind) {
    case 'ServiceAccount':
      return 'bg-green-500/20 text-green-400'
    case 'User':
      return 'bg-blue-500/20 text-blue-400'
    case 'Group':
      return 'bg-purple-500/20 text-purple-400'
    default:
      return 'bg-gray-500/20 text-gray-400'
  }
}

function getRoleRefKindBadgeClass(kind: string): string {
  switch (kind) {
    case 'Role':
      return 'bg-blue-500/20 text-blue-400'
    case 'ClusterRole':
      return 'bg-purple-500/20 text-purple-400'
    default:
      return 'bg-gray-500/20 text-gray-400'
  }
}

export function RoleBindingRenderer({ data, onNavigate }: RoleBindingRendererProps) {
  const roleRef = data.roleRef || {}
  const subjects: any[] = data.subjects || []
  const isClusterRoleBinding = data.kind === 'ClusterRoleBinding'

  return (
    <>
      {subjects.length === 0 && (
        <AlertBanner
          variant="warning"
          title="No Subjects"
          message="This binding has no subjects — it has no effect until subjects are added."
        />
      )}

      {isClusterRoleBinding && (
        <div className="p-2 bg-blue-500/10 border border-blue-500/30 rounded text-xs text-blue-300/80 flex items-start gap-2">
          <span>ClusterRoleBinding grants permissions across all namespaces.</span>
        </div>
      )}

      <Section title="Role Reference" icon={Shield}>
        <PropertyList>
          <Property
            label="Kind"
            value={
              roleRef.kind ? (
                <span className={clsx('badge', getRoleRefKindBadgeClass(roleRef.kind))}>
                  {roleRef.kind}
                </span>
              ) : undefined
            }
          />
          <Property label="Name" value={
            roleRef.name ? <ResourceLink name={roleRef.name} kind={roleRef.kind === 'ClusterRole' ? 'clusterroles' : 'roles'} namespace={roleRef.kind === 'ClusterRole' ? '' : (data.metadata?.namespace || '')} onNavigate={onNavigate} /> : undefined
          } />
          <Property label="API Group" value={roleRef.apiGroup} />
        </PropertyList>
      </Section>

      <Section title={`Subjects (${subjects.length})`} icon={Users} defaultExpanded>
        <div className="space-y-2">
          {subjects.map((subject: any, i: number) => (
            <div key={`${subject.kind}-${subject.name}-${i}`} className="card-inner text-sm">
              <div className="flex items-center gap-2">
                <span className={clsx('badge', getSubjectKindBadgeClass(subject.kind))}>
                  {subject.kind}
                </span>
                {subject.kind === 'ServiceAccount' ? (
                  <ResourceLink name={subject.name} kind="serviceaccounts" namespace={subject.namespace || 'default'} onNavigate={onNavigate} />
                ) : (
                  <span className="text-theme-text-primary font-medium">{subject.name}</span>
                )}
              </div>
              <div className="text-xs text-theme-text-tertiary mt-1">
                Namespace: {subject.kind === 'ServiceAccount' ? (subject.namespace || 'default') : '-'}
              </div>
            </div>
          ))}
        </div>
      </Section>
    </>
  )
}
