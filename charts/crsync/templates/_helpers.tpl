{{/*
Common labels applied to all resources.
*/}}
{{- define "crsync.labels" -}}
app.kubernetes.io/name: crsync
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels used by the Deployment and ServiceAccount.
*/}}
{{- define "crsync.selectorLabels" -}}
app.kubernetes.io/name: crsync
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
