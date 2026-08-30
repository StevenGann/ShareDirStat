{{/*
Expand the name of the chart.
*/}}
{{- define "sharedirstat.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
A fully qualified app name, truncated to the 63-character DNS limit.
*/}}
{{- define "sharedirstat.fullname" -}}
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

{{- define "sharedirstat.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "sharedirstat.labels" -}}
helm.sh/chart: {{ include "sharedirstat.chart" . }}
{{ include "sharedirstat.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "sharedirstat.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sharedirstat.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
The share id derived from a share's name: lower-case, invalid characters
replaced, matching what the application's auto-discovery would produce.
*/}}
{{- define "sharedirstat.shareId" -}}
{{- /* regexReplaceAll takes (regex, subject, replacement): the subject cannot be piped in. */}}
{{- regexReplaceAll "[^a-z0-9_-]" (lower .) "-" | trimAll "-" | trunc 64 }}
{{- end }}
