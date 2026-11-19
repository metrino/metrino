{{- if .Values.accessPolicy.create }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Values.trino.opa.policyConfigMap }}
  labels:
    {{- include "metrino.labels" . | nindent 4 }}

data:
  {{- (.Files.Glob "opa/*.rego").AsConfig | nindent 2 }}
  {{- with .Values.accessPolicy.rules }}
  rules.rego: |-
    {{- . | nindent 4 }}
  {{- end }}
{{- end }}
