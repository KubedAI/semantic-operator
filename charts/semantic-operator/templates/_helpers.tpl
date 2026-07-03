{{- define "semantic-operator.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "semantic-operator.labels" -}}
app.kubernetes.io/name: {{ include "semantic-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "semantic-operator.managerSA" -}}
{{- default (printf "%s-manager" (include "semantic-operator.name" .)) .Values.serviceAccount.manager.name -}}
{{- end -}}

{{- define "semantic-operator.serverSA" -}}
{{- default (printf "%s-server" (include "semantic-operator.name" .)) .Values.serviceAccount.server.name -}}
{{- end -}}

{{/* Shared StarRocks env for both deployments. */}}
{{- define "semantic-operator.starrocksEnv" -}}
- name: STARROCKS_HOST
  value: {{ required "starrocks.host is required" .Values.starrocks.host | quote }}
- name: STARROCKS_PORT
  value: {{ .Values.starrocks.port | quote }}
- name: STARROCKS_USER
  value: {{ .Values.starrocks.user | quote }}
{{- if .Values.starrocks.passwordSecret.name }}
- name: STARROCKS_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.starrocks.passwordSecret.name }}
      key: {{ .Values.starrocks.passwordSecret.key }}
{{- end }}
{{- end -}}
