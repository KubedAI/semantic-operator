---
title: Govern a model with OPA
description: A curl walkthrough on Trino. Deploy the operator with an Open Policy Agent decision engine, then see two authorization layers apply to one query, the model's built-in governance and an external OPA decision.
---

Each model carries its own governance rules. Its roles decide which metrics and fields a
caller may read, and a row filter can narrow the rows a caller sees. This walkthrough adds a
second, independent decision on top, from an [Open Policy Agent](https://www.openpolicyagent.org/)
(OPA) server. A request must pass both the OPA policy and the model's own rules before any SQL
runs. [Authoring a model](/guides/authoring) covers how a model opts in.

## Prerequisites

- Docker, `kubectl`, `helm`, `kind`, and GNU `make`.
- `curl` and `jq` for the requests.

## Bring up the stack

One command builds a local kind cluster and deploys the whole example:

```bash
make models-deploy KIND_ENGINE_TYPE=trino
export KUBECONFIG="$PWD/.kube/config"
```

When it finishes, the cluster holds the following:

```
kind cluster
└─ semantic-system
   ├─ Keycloak        installed as a Trino prerequisite, unused in header mode
   ├─ Trino (TLS)     query engine. Holds the retail data in memory.osi_demo.
   ├─ OPA             policy engine. Serves a retail decision at
   │                  /v1/data/semantic/retail/decision.
   └─ operator        manager (compiles and publishes the model) and
                      server (answers queries)
```

The last line of the command shows the model reconciled:

```
NAME           VERSION        VALIDATED   PUBLISHED   DRIFT   AGE
tpcds-retail   25d972604ecf   True        True        False   12s
```

Two settings shape what you will see. The server runs in `static` engine identity mode, so it
connects to Trino with its own credential and every query runs as one database user. That
leaves all per-caller governance to the semantic layer and OPA, not to Trino. The server also
runs in `header` authentication mode, so it reads the caller identity from request headers.
Header mode trusts whatever sends those headers, so a real deployment puts an authenticating
proxy in front that strips any client-supplied identity headers and sets them from the
verified caller. This local stack sets `allowInsecureHeaderAuth` to accept them directly.

This stack is for local use only. The OPA endpoint is plaintext, the server skips Trino
certificate verification, and header mode accepts the identity headers without a proxy. A real
deployment uses TLS everywhere and an authenticating proxy.

## The two authorization layers

Every query passes through two checks before the server emits SQL. The server evaluates the
external provider first, then the model's built-in governance. A denial at either returns 403
and no SQL runs.

**The model's built-in governance.** The retail model defines three roles:

- `analyst` may read every metric but not the field `customer.c_email_address`.
- `tx_analyst` has the same rules and also sees only rows where `store.s_state` is `TX`.
- `admin` has no restrictions.

**The external OPA decision.** The model names a provider, `retail-opa`. For each request the
server sends OPA the caller principal and roles, the action, the model identity, and the
request shape, then requires an `allow` in response. The example policy in
`test/e2e/opa/retail.rego` allows a request when, among other checks:

- the request is a `query` action from the REST adapter,
- the principal is `demo-user`,
- a role is one of `analyst`, `tx_analyst`, or `admin`,
- the model matches the retail model by namespace, resource, and name,
- the request names at least one metric.

The policy also checks the request `apiVersion` and a positive access time. Read the Rego in
`test/e2e/opa/retail.rego` for the full set.

The two checks are independent. A model author owns the built-in rules, and a platform team
can own the OPA policy. Once a model references a provider, the platform team can change that
policy without editing the model.

## Port-forward the server

The server runs inside the cluster, so forward its port to your machine:

```bash
kubectl -n semantic-system port-forward svc/semantic-operator-server 19000:8090 &
sleep 3
```

## An allowed query

Send a query as `demo-user` with the `analyst` role. Ask for `total_sales` by state, which
uses no restricted field:

```bash
curl -s -X POST http://127.0.0.1:19000/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: demo-user' -H 'X-Semantic-Role: analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["store.s_state"],"limit":3}' | jq
```

Both checks allow it, so the server returns the rows and the SQL it ran. The response below is
abridged. The full body also includes the column names, the row count, and timing fields:

```json
{
  "rows": [["CA", "599773.53"], ["GA", "526715.02"]],
  "sql": "/* semantic-layer model=tpcds_retail_model version=25d972604ecf request=5de244dd69b15bb551592b8ba652e9d2 */\nSELECT \"store\".\"s_state\" ... FROM \"memory\".\"osi_demo\".\"store_sales\" ..."
}
```

The leading comment records the model, the compiled version, and a request hash, so every
statement the engine runs can be traced back to a request. The query ran under the server's
own Trino credential, because the stack is in `static` mode.

## A denial from the model

Keep the same caller and ask for the email field. The `analyst` role denies it:

```bash
curl -s -X POST http://127.0.0.1:19000/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: demo-user' -H 'X-Semantic-Role: analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["customer.c_email_address"],"limit":3}'
```

```json
{"error": "unauthorized: role \"analyst\" may not read field \"customer.c_email_address\""}
```

The status is 403. OPA allowed this request, because it checks the caller, the model, and the
request shape, not individual fields. The model's own governance then rejected the email
field, before any SQL existed. The error names the field, so you can see that the model's
rules made the decision.

## A denial from OPA

Now change only the user, from `demo-user` to `mallory`, and run the allowed query again:

```bash
curl -s -X POST http://127.0.0.1:19000/v1/models/tpcds_retail_model/query \
  -H 'X-Semantic-User: mallory' -H 'X-Semantic-Role: analyst' \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["total_sales"],"dimensions":["store.s_state"],"limit":3}'
```

```json
{"error": "unauthorized: external provider \"retail-opa\" denied action \"query\""}
```

The status is 403 again, but for a different reason. OPA runs first and rejected this request,
because the principal is not `demo-user`. The model's own rules would have allowed it, since
`analyst` may read `total_sales` by state, but the request never reached them. The error names
the provider that denied, so you can tell the two checks apart in logs and audits.

## Where the policy and provider live

- The policy is the Rego in `test/e2e/opa/retail.rego`, loaded into the cluster as the
  `opa-retail-policy` ConfigMap. Edit it and rerun `make opa-deploy` to change the decision.
- The provider is configured by the server administrator in the Helm values under
  `server.authorization.providers`, with its type, URL, and timeout.
- The model opts in with one field, `spec.governance.external.providerRef`, pointing at the
  provider name. The shared retail model in `examples/retail/model/semanticmodel.yaml` has no
  provider by default. `make models-deploy` merges the patch in
  `test/e2e/models/trino-opa-patch.yaml`, which adds that field, before publishing.

## Ranger

Apache Ranger plugs in the same way, as a provider of type `ranger`. `make ranger-deploy`
stands up a minimal Ranger Admin and policy decision point, and a model references it through
the same `providerRef` field. A dedicated Ranger walkthrough will follow.

## Next

[Authoring a model](/guides/authoring) shows the external decision gate in the model spec.
[The semantic server](/architecture/server) describes where the check sits in the request
pipeline. [Configuration and deployment](/reference/configuration) covers the provider
settings and the audit record.
