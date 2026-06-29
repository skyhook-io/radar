// Wire types for the /api/rbac/* endpoints, kept in the shared package so
// both the host (which fetches) and the renderers (which display) agree on
// the shape. Mirrors the Go types in internal/server/rbac_handlers.go and
// pkg/rbac/index.go.

export interface RBACSubject {
  kind: string // "ServiceAccount" | "User" | "Group"
  namespace: string // empty for User/Group
  name: string
}

export interface RBACRoleRef {
  kind: string // "Role" | "ClusterRole"
  namespace: string // empty for ClusterRole
  name: string
}

export interface RBACBindingRef {
  kind: string // "RoleBinding" | "ClusterRoleBinding"
  namespace: string // empty for ClusterRoleBinding
  name: string
  roleRef: RBACRoleRef
}

export interface RBACPolicyRule {
  verbs?: string[]
  apiGroups?: string[]
  resources?: string[]
  resourceNames?: string[]
  nonResourceURLs?: string[]
}

export interface RBACBindingRules {
  binding: RBACBindingRef
  role: RBACRoleRef
  rules: RBACPolicyRule[]
  /** Populated when a RoleBinding references a ClusterRole — rules apply
   *  only in this namespace. */
  scopeNamespace?: string
}

export interface RBACInheritedGroup {
  groupName: string // e.g. "system:authenticated"
  bindings: RBACBindingRules[]
}

export interface RBACSubjectResponse {
  subject: RBACSubject
  direct: RBACBindingRules[]
  inheritedFromGroups: RBACInheritedGroup[]
  flat: RBACPolicyRule[]
  truncated: boolean
  /** Pods whose spec.serviceAccountName references this SA. Populated only
   *  for ServiceAccount subjects; undefined or empty for User/Group. */
  usedByPods?: RBACPodRef[]
}

export interface RBACPodRef {
  namespace: string
  name: string
}

export interface RBACNamespaceResponse {
  namespace: string
  roleBindings: RBACBindingWithSubjects[]
  /** ClusterRoleBindings with at least one subject in this namespace. */
  clusterRoleBindingsWithLocalSubject: RBACBindingWithSubjects[]
  serviceAccountCount: number
}

export interface RBACBindingWithSubjects {
  binding: RBACBindingRef
  subjects: RBACSubject[]
}

export interface RBACRoleResponse {
  role: RBACRoleRef
  bindings: RBACBindingWithSubjects[]
}

export interface RBACResourceRule {
  verbs: string[]
  apiGroups?: string[]
  resources?: string[]
  resourceNames?: string[]
}

export interface RBACNonResourceRule {
  verbs: string[]
  nonResourceURLs?: string[]
}

export interface RBACWhoamiResponse {
  namespace: string
  resourceRules: RBACResourceRule[]
  nonResourceRules: RBACNonResourceRule[]
  incomplete: boolean
  evaluationError?: string
}
