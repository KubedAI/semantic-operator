# DataHub + Polaris + StarRocks on kind

The whole stack on a laptop: Garage, Polaris, StarRocks, DataHub, the operator,
a local HTTPS gateway, and Keycloak.

**Full documentation: https://kubedai.github.io/semantic-operator/examples/kind**

The files in this directory are the runnable parts. The general instructions live
on the docs site. The local HTTPS and identity foundation is documented here.

## Lifecycle

Run the stages in order. The gateway must exist before Keycloak because the
public issuer URL resolves through Caddy from both the host and cluster pods.

```bash
make cluster-up images-load
make garage-up postgres-up
make polaris-up polaris-catalog
make starrocks-up starrocks-catalog data-load
make operator-build operator-up opa-up models-apply
make gateway-up
make keycloak-up
make datahub-up datahub-ingest datahub-enrich
make datahub-mcp-build datahub-mcp-up

# Point your own MCP client (Claude Code, Kiro CLI, or any MCP host) at the
# Semantic Operator MCP and the DataHub MCP. The docs page has the exact,
# directory-scoped registration.

make cluster-down
```

`make keycloak-up` depends on `gateway-up`, and `make datahub-up` depends on
`keycloak-up`, so the OIDC issuer and local trust chain exist before the
DataHub frontend starts. Recreate an existing kind cluster after changing host
mappings. Persisted Garage and Postgres data under `data/` survives
`make cluster-down`.

## Normal HTTPS access

The normal user entry points are:

```text
https://datahub.localtest.me:8443
https://auth.localtest.me:8443
```

DataHub authenticates browser users through the `saas-accounts` Keycloak
realm and provisions their DataHub users on first login.

This local stack explicitly sets `networkPolicy.enabled=false` for Semantic
Operator. That keeps the loopback-bound `127.0.0.1:8090` debug path usable
while header authentication is still enabled.
It also means every pod in this single-user demo cluster can reach the semantic
server. Do not copy this override to a shared cluster. Set it back to `true` to
restore the chart's default ingress lockdown.

`gateway-up` creates a local CA and wildcard leaf certificate under `.tmp/tls/`
unless all three custom paths are supplied as `GATEWAY_TLS_CERT`,
`GATEWAY_TLS_KEY`, and `GATEWAY_CA_CERT`. Only the leaf key and certificate enter
Kubernetes. The CA private key stays on the host. A stable copy of the active
public CA is `.tmp/tls/trust-ca.crt`. It is published as `account-demo/gateway-ca` and
mirrored to `datahub/gateway-ca` so the frontend can validate OIDC HTTPS calls.

For one-off clients, use `--cacert .tmp/tls/trust-ca.crt`. To trust it on a local
Linux workstation, copy the public CA into the system trust directory and
refresh the trust store according to the workstation distribution. Never copy
or trust `.tmp/tls/ca.key` on another machine.

Demo user passwords and confidential client secrets are generated without being
printed. Inspect the mode-0600 file `.tmp/keycloak/demo-credentials.env` when
needed. Preserve this file with the persisted Postgres data. Realm changes are
reconciled while Keycloak is stopped. The file and all TLS material are ignored
by Git.

## Loopback debug and development access

The kind node exposes fixed NodePorts through host mappings that bind explicitly
to `127.0.0.1`. They provide convenient development and demo access alongside
the HTTPS gateway:

| Host endpoint | Component |
|---|---|
| `127.0.0.1:3900` | Garage S3 API |
| `127.0.0.1:8181` | Polaris REST catalog |
| `127.0.0.1:9030` | StarRocks FE MySQL protocol |
| `127.0.0.1:8030` | StarRocks FE HTTP |
| `127.0.0.1:8090` | Semantic Operator MCP and REST |
| `127.0.0.1:8080` | DataHub GMS |
| `127.0.0.1:8091` | DataHub MCP |
| `127.0.0.1:9002` | DataHub frontend debug URL |
| `127.0.0.1:8443` | HTTPS gateway |

Component lifecycle scripts apply their NodePort Services. No background
`kubectl port-forward` processes are required. The direct endpoints are
intentional alternate development paths; HTTPS remains the normal browser path.

## SSH access

When the cluster runs on a remote Linux host, first copy only the public CA:

```bash
scp ubuntu@<remote-host>:<stack-path>/.tmp/tls/trust-ca.crt ./saas-accounts-local-ca.crt
```

Use this loopback-only tunnel for the normal HTTPS entry point:

```bash
ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:8443:127.0.0.1:8443 \
  ubuntu@<remote-host>
```

Add selected debug ports to the same command when needed, for example:

```bash
ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:8443:127.0.0.1:8443 \
  -L 127.0.0.1:8090:127.0.0.1:8090 \
  -L 127.0.0.1:8091:127.0.0.1:8091 \
  ubuntu@<remote-host>
```

Do not use SSH `-g` or `GatewayPorts`. The `localtest.me` names resolve to
`127.0.0.1` on the workstation, so browser traffic enters the SSH tunnel. Inside
kind, `gateway-up` maintains a marked CoreDNS `NodeHosts` block that resolves the
same names to the gateway ClusterIP. This split DNS keeps the canonical issuer
`https://auth.localtest.me:8443/realms/saas-accounts` identical for host and
pod callers.
