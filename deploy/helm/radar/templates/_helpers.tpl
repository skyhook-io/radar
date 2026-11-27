{{/*
Expand the name of the chart.
*/}}
{{- define "radar.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "radar.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "radar.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "radar.labels" -}}
helm.sh/chart: {{ include "radar.chart" . }}
{{ include "radar.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "radar.selectorLabels" -}}
app.kubernetes.io/name: {{ include "radar.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "radar.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "radar.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
The URL path prefix Radar serves under: "" for root, otherwise "/<prefix>"
with no trailing slash. Shared by the Deployment (--base-path, probes) and
NOTES.txt (the URL printed after install).
*/}}
{{- define "radar.basePath" -}}
{{- $trimmed := trimAll "/" (default "" .Values.basePath) -}}
{{- if $trimmed -}}
{{- printf "/%s" $trimmed -}}
{{- end -}}
{{- end }}

{{- define "radar.operatorSettings" -}}
{{- $settings := dict "version" 1 -}}
{{- if .Values.audit -}}
{{- $_ := set $settings "audit" .Values.audit -}}
{{- end -}}
{{- if .Values.helm.ociSources -}}
{{- $_ := set $settings "helmOciSources" .Values.helm.ociSources -}}
{{- end -}}
{{- $settings | toPrettyJson -}}
{{- end -}}

{{/*
Whether the default read grant for Radar Cloud's background services
(radar:system) renders. It includes cluster-wide Secret read, so an absent
value means OFF: a `--reuse-values` upgrade from a release that predates the
key renders with the previous release's tree and never gains Secret read
without someone choosing it. Fresh installs, plain upgrades, GitOps and
`--reset-then-reuse-values` get true from values.yaml.
*/}}
{{- define "radar.cloudSystemRbac" -}}
{{- if and .Values.cloud.enabled (eq (toString .Values.cloud.systemRbac) "true") -}}
true
{{- else -}}
false
{{- end -}}
{{- end -}}

{{/*
Whether the default read grant for automatic Diagnose (radar:ai:reader) renders.
An absent value means OFF, as with radar.cloudSystemRbac: a
`--reuse-values` upgrade from a release that predates the key never gains a
new reader without someone choosing it.
*/}}
{{- define "radar.cloudAiRbac" -}}
{{- if and .Values.cloud.enabled (eq (toString .Values.cloud.aiRbac) "true") -}}
true
{{- else -}}
false
{{- end -}}
{{- end -}}

{{/*
Whether Radar calls Kubernetes with each person's own token instead of
impersonating them (auth.kubernetesCredentials=user-token).
*/}}
{{- define "radar.userTokenMode" -}}
{{- if eq (.Values.auth.kubernetesCredentials | default "impersonate") "user-token" -}}
true
{{- else -}}
false
{{- end -}}
{{- end -}}

{{/*
Enrollment mode: Radar joins cloud.hubURL with a shared enrollment key every
time it starts and holds the token it gets in memory. A configured cloud.token or
cloud.existingSecret wins, so a token install renders exactly as before.
*/}}
{{- define "radar.cloudEnrollmentMode" -}}
{{- $c := .Values.cloud -}}
{{- $e := $c.enrollment | default dict -}}
{{- if and $c.enabled $c.hubURL (or $e.key $e.existingSecret) (not (or $c.token $c.existingSecret)) -}}
true
{{- else -}}
false
{{- end -}}
{{- end -}}

{{- define "radar.cloudEnrollmentKeySecretName" -}}
{{- $e := .Values.cloud.enrollment | default dict -}}
{{- $e.existingSecret | default (printf "%s-cloud-enrollment-key" (include "radar.fullname" .)) -}}
{{- end -}}

{{/*
A binding to a ClusterRole: one ClusterRoleBinding, or with rbac.namespaces
one RoleBinding per listed namespace, which grants the role's namespaced rules
only there. Takes root, name, roleRef, and groups and/or serviceAccount.
*/}}
{{- define "radar.binding" -}}
{{- range $namespace := .root.Values.rbac.namespaces | default (list "") }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: {{ if $namespace }}RoleBinding{{ else }}ClusterRoleBinding{{ end }}
metadata:
  name: {{ $.name }}
  {{- with $namespace }}
  namespace: {{ . | quote }}
  {{- end }}
  labels:
    {{- include "radar.labels" $.root | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ $.roleRef }}
subjects:
  {{- with $.serviceAccount }}
  - kind: ServiceAccount
    name: {{ .name }}
    namespace: {{ .namespace }}
  {{- end }}
  {{- range $.groups }}
  - kind: Group
    name: {{ . }}
    apiGroup: rbac.authorization.k8s.io
  {{- end }}
{{- end }}
{{- end -}}

{{/*
Cloud tier groups. Each tier also binds its pre-rename cloud:* group so
rolling hub and chart upgrades stay compatible; remove those in v2.
*/}}
{{- define "radar.tierGroups" -}}
{{- if has . (list "owner" "member" "viewer") -}}
{{- list (printf "radar:%s" .) (printf "cloud:%s" .) | toJson -}}
{{- else if eq . "ai" -}}
{{- list "radar:ai:reader" | toJson -}}
{{- else -}}
{{- list (printf "radar:%s" .) | toJson -}}
{{- end -}}
{{- end -}}

{{- /*
The roles and the agent admission list must come from the same chart gates.
*/}}
{{- define "radar.ownerAccessPolicy" -}}
{{- $grants := dict -}}
{{- $blocked := dict -}}
{{- $rbac := .Values.cloud.defaultRbac -}}
{{- $builtInOwner := has $rbac.ownerClusterRole (list "admin" "cluster-admin") -}}
{{- $mode := "kubernetes" -}}
{{- /* Helm can skip schema validation. Strings must never turn a write gate on. */ -}}
{{- $managedRoles := ne (include "radar.userTokenMode" .) "true" -}}
{{- range $gate := list .Values.cloud.enabled .Values.rbac.create $rbac.create -}}
{{- $managedRoles = and $managedRoles (kindIs "bool" $gate) $gate -}}
{{- end -}}
{{- if and $managedRoles (kindIs "bool" $rbac.owner) (not $rbac.owner) -}}
{{- $mode = "owner-disabled" -}}
{{- end -}}
{{- if and $managedRoles (kindIs "bool" $rbac.owner) $rbac.owner -}}
{{- $mode = ternary "standard" "custom-owner" $builtInOwner -}}
{{- $policy := .Files.Get "files/integration-write-baseline.yaml" | fromYaml -}}
{{- $switches := $rbac.integrationWrite -}}
{{- $enabled := dict -}}
{{- if or (kindIs "invalid" $switches) (kindIs "map" $switches) -}}
{{- range $capability, $spec := $policy.capabilities -}}
{{- $switch := index ($switches | default dict) $capability -}}
{{- if or (kindIs "invalid" $switch) (kindIs "map" $switch) -}}
{{- $owner := index ($switch | default dict) "owner" -}}
{{- if kindIs "invalid" $owner -}}
{{- $owner = and $spec.default (or (ne $capability "cnpg") $builtInOwner) -}}
{{- end -}}
{{- $_ := set $enabled $capability (and (kindIs "bool" $owner) $owner) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $collections := .Values.rbac.crdGroups -}}
{{- $all := and (kindIs "map" $collections) (kindIs "bool" $collections.all) $collections.all -}}
{{- range $entry := $policy.entries -}}
{{- $collect := false -}}
{{- if kindIs "map" $collections -}}
{{- $collection := index $collections $entry.collection -}}
{{- $collect = or $all (and (kindIs "bool" $collection) $collection) -}}
{{- end -}}
{{- if and (eq $entry.collection "velero") (or (not (empty $.Values.rbac.namespaces)) (not $builtInOwner)) -}}
{{- $_ := set $blocked $entry.capability "velero-scope" -}}
{{- else if not $collect -}}
{{- $_ := set $blocked $entry.capability "collection" -}}
{{- else if not (index $enabled $entry.capability) -}}
{{- $_ := set $blocked $entry.capability "helm" -}}
{{- else -}}
{{- $rule := dict "apiGroups" (list $entry.group) "resources" $entry.resources "verbs" $entry.verbs -}}
{{- $_ := set $grants $entry.capability (append (index $grants $entry.capability | default list) $rule) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toJson (dict "grants" $grants "setup" (dict "mode" $mode "blocked" $blocked)) -}}
{{- end -}}

{{- define "radar.ownerAccessGrants" -}}
{{- toJson ((include "radar.ownerAccessPolicy" . | fromJson).grants) -}}
{{- end -}}
