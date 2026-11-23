{{/*
Expand the name of the chart.
*/}}
{{- define "metrino.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "metrino.fullname" -}}
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
{{- define "metrino.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "metrino.labels" -}}
helm.sh/chart: {{ include "metrino.chart" . }}
{{ include "metrino.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "metrino.selectorLabels" -}}
app.kubernetes.io/name: {{ include "metrino.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "metrino.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "metrino.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* 
Create the name of the postgresql password secret 
*/}}
{{- define "metrino.postgresql.secretName" -}}
{{- if .Values.postgresql.auth.existingSecret -}}
{{ .Values.postgresql.auth.existingSecret -}}
{{- else -}}
{{ printf "%s-postgresql" .Release.Name -}}
{{- end -}}
{{- end -}}

{{/*
Trino's in-cluster URL. Uses the Trino chart's own name helper, given the Trino values, so it follows
the Service that chart renders.
*/}}
{{- define "metrino.trino.url" -}}
{{- $trino := dict "Values" .Values.trino "Release" .Release "Chart" (dict "Name" "trino") -}}
{{- printf "http://%s:%v" (include "trino.fullname" $trino) .Values.trino.service.port -}}
{{- end -}}

{{/*
The Metabase Service's name (metabase-service.yaml). Uses only .Release, so it also works from the Trino chart's
tpl'd values (trino.initContainers in values.yaml), where .Values is the Trino chart's.
*/}}
{{- define "metrino.metabase.serviceName" -}}
{{- printf "%s-metabase-service" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Metabase's in-cluster URL: the Service's port 80, which goes to Metabase's 3000. Like the name, uses only .Release.
*/}}
{{- define "metrino.metabase.url" -}}
{{- printf "http://%s" (include "metrino.metabase.serviceName" .) -}}
{{- end -}}

{{/*
The web Service's name (web.yaml), which the web Ingress sends to.
*/}}
{{- define "metrino.web.serviceName" -}}
{{- printf "%s-web" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
The Prometheus server's in-cluster URL, from the Prometheus chart's name helper, so it follows the Service that
chart renders. No path: the web server sends each request's own /prometheus/ path.
*/}}
{{- define "metrino.prometheus.url" -}}
{{- $prometheus := dict "Values" .Values.prometheus "Release" .Release "Chart" (dict "Name" "prometheus") -}}
{{- printf "http://%s:%v" (include "prometheus.server.fullname" $prometheus) .Values.prometheus.server.service.servicePort -}}
{{- end -}}
