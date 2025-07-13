{{/*
Expand the name of the chart.
*/}}
{{- define "kube-janitor-go.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "kube-janitor-go.fullname" -}}
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
{{- define "kube-janitor-go.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kube-janitor-go.labels" -}}
helm.sh/chart: {{ include "kube-janitor-go.chart" . }}
{{ include "kube-janitor-go.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kube-janitor-go.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-janitor-go.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kube-janitor-go.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kube-janitor-go.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Build the command-line arguments for kube-janitor-go
*/}}
{{- define "kube-janitor-go.args" -}}
{{- $args := list }}
{{- $args = append $args (printf "--interval=%s" .Values.janitor.interval) }}
{{- $args = append $args (printf "--log-level=%s" .Values.janitor.logLevel) }}
{{- $args = append $args (printf "--max-workers=%d" (int .Values.janitor.maxWorkers)) }}
{{- $args = append $args (printf "--metrics-port=%d" (int .Values.metrics.port)) }}
{{- if .Values.janitor.dryRun }}
{{- $args = append $args "--dry-run" }}
{{- end }}
{{- if .Values.janitor.runOnce }}
{{- $args = append $args "--once" }}
{{- end }}
{{- if .Values.janitor.includeResources }}
{{- $args = append $args (printf "--include-resources=%s" (join "," .Values.janitor.includeResources)) }}
{{- end }}
{{- if .Values.janitor.excludeResources }}
{{- $args = append $args (printf "--exclude-resources=%s" (join "," .Values.janitor.excludeResources)) }}
{{- end }}
{{- if .Values.janitor.includeNamespaces }}
{{- $args = append $args (printf "--include-namespaces=%s" (join "," .Values.janitor.includeNamespaces)) }}
{{- end }}
{{- if .Values.janitor.excludeNamespaces }}
{{- $namespaces := .Values.janitor.excludeNamespaces }}
{{- if not (has .Release.Namespace $namespaces) }}
{{- $namespaces = append $namespaces .Release.Namespace }}
{{- end }}
{{- $args = append $args (printf "--exclude-namespaces=%s" (join "," $namespaces)) }}
{{- end }}
{{- if .Values.janitor.rulesFile.enabled }}
{{- $args = append $args (printf "--rules-file=%s" .Values.janitor.rulesFile.path) }}
{{- end }}
{{ toYaml $args }}
{{- end }}

{{/*
Common annotations
*/}}
{{- define "kube-janitor-go.annotations" -}}
{{- with .Values.commonAnnotations }}
{{ toYaml . }}
{{- end }}
{{- end }} 