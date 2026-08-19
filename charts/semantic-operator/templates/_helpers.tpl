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

{{/*
  The image reference for one deployment. Call with a dict:
    (dict "root" . "component" "manager"|"server")
*/}}
{{- define "semantic-operator.image" -}}
{{- $root := .root -}}
{{- if $root.Values.image.repository -}}
{{- fail "image.repository has been replaced by image.manager.repository and image.server.repository, which are used verbatim. Set those instead." -}}
{{- end -}}
{{- $img := index $root.Values.image .component | default dict -}}
{{- $repo := required (printf "image.%s.repository is required" .component) $img.repository -}}
{{- $tag := $img.tag | default $root.Values.image.tag -}}
{{- printf "%s:%s" $repo (required "image.tag is required" $tag) -}}
{{- end -}}

{{/*
  Path the config file is mounted at, and the env var the binaries read it from.
*/}}
{{- define "semantic-operator.configPath" -}}/etc/semantic-operator/config.yaml{{- end -}}

{{/*
  Engine connection block of the config, as YAML. Call with a dict:
    (dict "root" . "component" "manager"|"server")

  engine.* is the source of truth for the endpoint. Credentials resolve per
  component so the two deployments can hold different database users: the
  manager needs metadata reads plus DDL on the views schema, the server needs
  SELECT only. The password is not rendered here; it is injected as env from a
  Secret (enginePasswordEnv). The legacy starrocks.* values are honored only
  when engine.type is starrocks.
*/}}
{{- define "semantic-operator.engineConfig" -}}
{{- $root := .root -}}
{{- $per := index $root.Values.engine .component | default dict -}}
{{- $engines := list "starrocks" "trino" -}}
{{- if not (has $root.Values.engine.type $engines) -}}
{{- fail (printf "engine.type %q is not supported. Supported engines are %s" $root.Values.engine.type (join ", " $engines)) -}}
{{- end -}}
{{- $legacy := eq $root.Values.engine.type "starrocks" -}}
{{- $host := $root.Values.engine.host -}}
{{- $user := $per.user | default $root.Values.engine.user -}}
{{- $port := $root.Values.engine.port -}}
{{- if $legacy -}}
{{- $host = default $root.Values.starrocks.host $host -}}
{{- $user = default $root.Values.starrocks.user $user -}}
{{- $port = default $root.Values.starrocks.port $port -}}
{{- end -}}
dialect: {{ $root.Values.engine.type | quote }}
connection:
  host: {{ required "engine.host is required (legacy starrocks.host applies only when engine.type is starrocks)" $host | quote }}
  {{- if $port }}
  port: {{ $port }}
  {{- end }}
  {{- if $user }}
  user: {{ $user | quote }}
  {{- end }}
  {{- if (($root.Values.engine.tls) | default dict).enabled }}
  tlsEnabled: true
  {{- end }}
  {{- if (($root.Values.engine.tls) | default dict).insecureSkipVerify }}
  tlsInsecureSkipVerify: true
  {{- end }}
{{- if eq .component "server" }}
{{- $mode := $root.Values.engine.identityMode | default "static" }}
identity:
  mode: {{ $mode | quote }}
  {{- if eq $mode "exchange" }}
  {{- $ex := $root.Values.engine.exchange | default dict }}
  exchange:
    tokenURL: {{ required "engine.exchange.tokenURL is required when engine.identityMode is exchange" $ex.tokenURL | quote }}
    clientID: {{ required "engine.exchange.clientID is required when engine.identityMode is exchange" $ex.clientID | quote }}
    {{- if $ex.allowInsecureHTTP }}
    allowInsecureHTTP: true
    {{- end }}
  {{- end }}
{{- end }}
{{- end -}}

{{/*
  The engine password as a SEMANTIC__ env entry from a Secret, or nothing when
  no Secret is configured. Call with a dict:
    (dict "root" . "component" "manager"|"server")
*/}}
{{- define "semantic-operator.enginePasswordEnv" -}}
{{- $root := .root -}}
{{- $per := index $root.Values.engine .component | default dict -}}
{{- $legacy := eq $root.Values.engine.type "starrocks" -}}
{{- /* Prefer the per-component secret, then the shared one, then (starrocks
       only) the legacy secret. Selection is by name, since the default value
       maps carry an empty name rather than being absent. */ -}}
{{- $sec := dict -}}
{{- if ($per.passwordSecret | default dict).name -}}
{{- $sec = $per.passwordSecret -}}
{{- else if (($root.Values.engine.passwordSecret) | default dict).name -}}
{{- $sec = $root.Values.engine.passwordSecret -}}
{{- else if and $legacy (($root.Values.starrocks.passwordSecret) | default dict).name -}}
{{- $sec = $root.Values.starrocks.passwordSecret -}}
{{- end -}}
{{- if $sec.name }}
- name: SEMANTIC__ENGINE__CONNECTION__PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ $sec.name }}
      key: {{ $sec.key }}
{{- end }}
{{- end -}}

{{/*
  External authorization providers, as the YAML list the server config expects.
  A provider's bearerTokenSecret is rewritten to a bearerTokenEnv reference; the
  matching token env is injected by the deployment (providerTokenEnv). URLs and
  credentials stay in deployment config so a model author cannot redirect
  authorization or read provider secrets.
*/}}
{{- define "semantic-operator.providerConfigs" -}}
{{- $providers := (.Values.server.authorization | default dict).providers | default list -}}
{{- $out := list -}}
{{- range $i, $p := $providers -}}
{{- $type := $p.type | default "opa" -}}
{{- $c := dict
      "name" (required (printf "server.authorization.providers[%d].name is required" $i) $p.name)
      "type" $type
      "url" (required (printf "server.authorization.providers[%d].url is required" $i) $p.url) -}}
{{- with $p.timeoutSeconds }}{{- $_ := set $c "timeoutSeconds" . }}{{- end -}}
{{- with $p.maxResponseBytes }}{{- $_ := set $c "maxResponseBytes" . }}{{- end -}}
{{- if eq $type "opa" -}}
{{- $opa := $p.opa | default dict -}}
{{- $_ := set $c "opa" (dict "decisionPath" (required (printf "server.authorization.providers[%d].opa.decisionPath is required" $i) $opa.decisionPath)) -}}
{{- else if eq $type "ranger" -}}
{{- $r := $p.ranger | default dict -}}
{{- $_ := set $c "ranger" (dict
      "authenticationMode" ($r.authenticationMode | default "service")
      "servicePrincipal" (required (printf "server.authorization.providers[%d].ranger.servicePrincipal is required" $i) $r.servicePrincipal)
      "allowInsecureHTTP" ($r.allowInsecureHTTP | default false)
      "serviceType" (required (printf "server.authorization.providers[%d].ranger.serviceType is required" $i) $r.serviceType)
      "serviceName" (required (printf "server.authorization.providers[%d].ranger.serviceName is required" $i) $r.serviceName)
      "resource" (required (printf "server.authorization.providers[%d].ranger.resource is required" $i) $r.resource)
      "permission" (required (printf "server.authorization.providers[%d].ranger.permission is required" $i) $r.permission)
      "contextAttributes" ($r.contextAttributes | default dict)) -}}
{{- else -}}
{{- fail (printf "server.authorization.providers[%d].type %q is not supported: use opa or ranger" $i $type) -}}
{{- end -}}
{{- $secret := $p.bearerTokenSecret | default dict -}}
{{- if $secret.name -}}
{{- $_ := set $c "bearerTokenEnv" (printf "AUTHORIZATION_PROVIDER_TOKEN_%d" $i) -}}
{{- end -}}
{{- $out = append $out $c -}}
{{- end -}}
{{ toYaml $out }}
{{- end -}}

{{/*
  The full server configuration document, mapping chart values onto the binary's
  config catalog. Non-secret only; secrets are injected as SEMANTIC__ env.
*/}}
{{- define "semantic-operator.serverConfig" -}}
{{- $auth := .Values.server.auth | default dict -}}
{{- $mode := $auth.mode | default "header" -}}
{{- if and (eq $mode "header") (not $auth.allowInsecureHeaderAuth) -}}
{{- fail "server.auth.mode=header trusts X-Semantic-User and X-Semantic-Role verbatim, so any client that reaches the service can assert an identity. Set server.auth.mode=jwt with a jwksURL for production, or set server.auth.allowInsecureHeaderAuth=true to confirm an authenticating proxy strips and sets both headers." -}}
{{- end -}}
{{- $http := .Values.server.http | default dict -}}
{{- $q := .Values.server.query | default dict -}}
{{- $providers := (.Values.server.authorization | default dict).providers | default list -}}
logging:
  level: {{ .Values.server.logLevel | default "info" | quote }}
server:
  listenAddr: ":{{ .Values.server.listenPort }}"
  {{- with $http.readTimeoutSeconds }}
  readTimeout: "{{ . }}s"
  {{- end }}
  {{- with $http.idleTimeoutSeconds }}
  idleTimeout: "{{ . }}s"
  {{- end }}
  {{- with $http.restTimeoutSeconds }}
  restTimeout: "{{ . }}s"
  {{- end }}
  {{- with $http.maxHeaderBytes }}
  maxHeaderBytes: {{ . }}
  {{- end }}
engine:
{{ include "semantic-operator.engineConfig" (dict "root" . "component" "server") | indent 2 }}
auth:
  mode: {{ $mode | quote }}
  {{- if eq $mode "jwt" }}
  jwksURL: {{ required "server.auth.jwksURL is required when auth.mode is jwt" $auth.jwksURL | quote }}
  {{- with $auth.issuer }}
  issuer: {{ . | quote }}
  {{- end }}
  {{- with $auth.audience }}
  audience: {{ . | quote }}
  {{- end }}
  {{- with $auth.principalClaim }}
  principalClaim: {{ . | quote }}
  {{- end }}
  {{- with $auth.roleClaim }}
  roleClaim: {{ . | quote }}
  {{- end }}
  {{- with $auth.groupsClaim }}
  groupsClaim: {{ . | quote }}
  {{- end }}
  {{- with $auth.claimsToCopy }}
  claimsToCopy:
{{ toYaml . | indent 4 }}
  {{- end }}
  {{- end }}
  {{- with .Values.engine.userClaim }}
  engineUserClaim: {{ . | quote }}
  {{- end }}
{{- $valkey := .Values.valkey | default dict }}
{{- if $valkey.addr }}
cache:
  addr: {{ $valkey.addr | quote }}
  db: {{ $valkey.db | default 0 }}
  planTTL: {{ .Values.server.planCacheTTL | quote }}
  resultTTL: {{ .Values.server.resultCacheTTL | quote }}
{{- end }}
{{- $limits := dict
      "defaultRowLimit" $q.defaultRowLimit
      "maxRowLimit" $q.maxRowLimit
      "maxMetrics" $q.maxMetrics
      "maxDimensions" $q.maxDimensions
      "maxFilters" $q.maxFilters
      "maxFilterValues" $q.maxFilterValues
      "maxResultBytes" $q.maxResultBytes
      "maxCacheEntryBytes" $q.maxCacheEntryBytes
      "maxRequestBytes" $q.maxRequestBytes
      "maxConcurrent" $q.maxConcurrent }}
{{- $query := dict }}
{{- range $k, $v := $limits }}{{- if $v }}{{- $_ := set $query $k $v }}{{- end }}{{- end }}
{{- with $q.queueWaitSeconds }}{{- $_ := set $query "queueWait" (printf "%vs" .) }}{{- end }}
{{- if $query }}
query:
{{ toYaml $query | indent 2 }}
{{- end }}
{{- if $providers }}
authorization:
  providers:
{{ include "semantic-operator.providerConfigs" . | indent 4 }}
{{- end }}
{{- with .Values.server.otelEndpoint }}
observability:
  otlpEndpoint: {{ . | quote }}
{{- end }}
store:
  watchNamespace: {{ .Release.Namespace | quote }}
  {{- if $q.exposeMetricExpressions }}
  exposeMetricExpressions: true
  {{- end }}
{{- end -}}

{{/*
  The full manager configuration document. Non-secret only.
*/}}
{{- define "semantic-operator.managerConfig" -}}
{{- include "semantic-operator.validateScope" . -}}
{{- $watched := include "semantic-operator.watchedNamespaces" . | fromYamlArray -}}
leaderElection:
  leaderElect: {{ .Values.manager.leaderElect }}
  resourceName: semantic-operator.semantic.ossie.io
{{- if $watched }}
cache:
  watchNamespaces:
{{ toYaml $watched | indent 4 }}
{{- end }}
engine:
{{ include "semantic-operator.engineConfig" (dict "root" . "component" "manager") | indent 2 }}
controller:
  viewDatabase: {{ .Values.manager.viewDatabase | quote }}
  resyncPeriod: {{ .Values.manager.resyncPeriod | quote }}
{{- end -}}

{{/*
  The namespaces the manager reconciles, as a YAML array. Empty watchNamespaces
  means the release namespace; cluster-wide is a separate explicit flag.
*/}}
{{- define "semantic-operator.watchedNamespaces" -}}
{{- if .Values.manager.watchAllNamespaces -}}
[]
{{- else if .Values.manager.watchNamespaces -}}
{{ toYaml .Values.manager.watchNamespaces }}
{{- else -}}
{{ toYaml (list .Release.Namespace) }}
{{- end -}}
{{- end -}}

{{- define "semantic-operator.validateScope" -}}
{{- if and .Values.manager.watchAllNamespaces .Values.manager.watchNamespaces -}}
{{- fail "manager.watchAllNamespaces and manager.watchNamespaces are mutually exclusive. Use watchNamespaces for a fixed list, or watchAllNamespaces for cluster-wide operation." -}}
{{- end -}}
{{- end -}}
