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
