{{/* Expand the name of the chart. */}}
{{- define "kube-op-lite.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "kube-op-lite.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "kube-op-lite.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Common labels. */}}
{{- define "kube-op-lite.labels" -}}
app.kubernetes.io/name: {{ include "kube-op-lite.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: kube-op-lite
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{/* Selector labels. */}}
{{- define "kube-op-lite.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-op-lite.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* Service account name. */}}
{{- define "kube-op-lite.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "kube-op-lite.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
