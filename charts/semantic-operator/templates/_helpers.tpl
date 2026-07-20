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

{{/* Shared query-engine env for both deployments. engine.* is the source of
     truth. The legacy starrocks.* values are honored ONLY when
     engine.type is "starrocks": StarRocks credentials, host, and port must
     never bleed into a Trino (or future engine) install, where a leaked
     password would force the client into HTTPS basic-auth mode and a leaked
     host would silently point at the wrong database. */}}
{{- define "semantic-operator.engineEnv" -}}
{{- $legacy := eq .Values.engine.type "starrocks" -}}
{{- $host := .Values.engine.host -}}
{{- $user := .Values.engine.user -}}
{{- $port := .Values.engine.port -}}
{{- $sec := .Values.engine.passwordSecret -}}
{{- if $legacy -}}
{{- $host = default .Values.starrocks.host $host -}}
{{- $user = default .Values.starrocks.user $user -}}
{{- $port = default .Values.starrocks.port $port -}}
{{- if not $sec.name -}}
{{- $sec = .Values.starrocks.passwordSecret -}}
{{- end -}}
{{- end -}}
- name: SQL_DIALECT
  value: {{ .Values.engine.type | quote }}
- name: ENGINE_HOST
  value: {{ required "engine.host is required (legacy starrocks.host applies only when engine.type is starrocks)" $host | quote }}
{{- if $port }}
- name: ENGINE_PORT
  value: {{ $port | quote }}
{{- end }}
{{- if $user }}
- name: ENGINE_USER
  value: {{ $user | quote }}
{{- end }}
{{- if $sec.name }}
- name: ENGINE_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ $sec.name }}
      key: {{ $sec.key }}
{{- end }}
{{- end -}}
