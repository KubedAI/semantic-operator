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

{{/* Query-engine env for one deployment. Call with a dict:
       (dict "root" . "component" "manager"|"server")

     engine.* is the source of truth for the endpoint. Credentials resolve
     per component so the two deployments can hold different database users:
     the manager needs metadata reads plus DDL on the views schema only, while
     the server needs SELECT on the model's tables and no DDL at all. Sharing
     one user gives the query path the ability to alter schemas. Set
     engine.manager.* / engine.server.* to split them; unset falls back to the
     shared engine.* credential.

     The legacy starrocks.* values are honored ONLY when engine.type is
     "starrocks": StarRocks credentials, host, and port must never bleed into
     a Trino (or future engine) install, where a leaked password would force
     the client into HTTPS basic-auth mode and a leaked host would silently
     point at the wrong database. */}}
{{- define "semantic-operator.engineEnv" -}}
{{- $root := .root -}}
{{- $per := index $root.Values.engine .component | default dict -}}
{{- /* Catch a typo here rather than in a CrashLoopBackOff. The binary only
       knows the engines registered with dbclient, so keep this list in step
       with them. */ -}}
{{- $engines := list "starrocks" "trino" -}}
{{- if not (has $root.Values.engine.type $engines) -}}
{{- fail (printf "engine.type %q is not supported. Supported engines are %s" $root.Values.engine.type (join ", " $engines)) -}}
{{- end -}}
{{- $legacy := eq $root.Values.engine.type "starrocks" -}}
{{- $host := $root.Values.engine.host -}}
{{- $user := $per.user | default $root.Values.engine.user -}}
{{- $port := $root.Values.engine.port -}}
{{- $sec := $per.passwordSecret | default $root.Values.engine.passwordSecret -}}
{{- if $legacy -}}
{{- $host = default $root.Values.starrocks.host $host -}}
{{- $user = default $root.Values.starrocks.user $user -}}
{{- $port = default $root.Values.starrocks.port $port -}}
{{- if not $sec.name -}}
{{- $sec = $root.Values.starrocks.passwordSecret -}}
{{- end -}}
{{- end -}}
{{- with $root -}}
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
{{- end -}}
