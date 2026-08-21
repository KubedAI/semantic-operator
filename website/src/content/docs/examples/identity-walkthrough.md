---
title: See identity propagation by hand
description: A curl walkthrough on Trino. Mint a Keycloak token, read its claims, and see the same query return different results for each caller as the engine enforces policy under that caller's identity.
---

This walkthrough shows identity propagation step by step. You mint a Keycloak
token, read its claims, and send it to the server. You then see the same query return a
different result for different users, because the engine runs each query under the
caller's identity. The concept behind it is on [Identity and the engine](/architecture/identity).

The auth end-to-end suite, described in [Developing](/guides/developing), automates every
check below. This page is the manual counterpart, for when you want to see the token and the
responses yourself.

## Prerequisites

- Docker, `kubectl`, `helm`, `kind`, and GNU `make`.
- `curl` and `jq` for the token and the requests.

## Bring up the stack

This walkthrough runs the whole identity path on your laptop, so you can follow a token as it
travels from the identity provider, through the server, to the query engine. One command
builds a local kind cluster and wires the pieces together:

```bash
make e2e-deploy KIND_ENGINE_TYPE=trino
export KUBECONFIG="$PWD/.kube/config"
```

When it finishes, the cluster holds the following:

```
kind cluster
├─ semantic-system         shared infrastructure
│   ├─ Keycloak            identity provider. Issues the tokens users log in with.
│   └─ Trino (TLS)         query engine. Holds the built-in tpch data and per-user rules.
│
├─ sem-static              operator release, engine identity mode = static
├─ sem-passthrough         operator release, engine identity mode = passthrough
└─ sem-exchange            operator release, engine identity mode = exchange
       each release is one manager (compiles models) and one server (answers queries)
```

Keycloak and Trino are shared. The operator is installed three times, once per engine
identity mode. The engine identity mode decides whose identity a query runs under when it
reaches Trino, and it is fixed for a release. Running all three side by side lets you send the
same query to each and compare. [Identity and the engine](/architecture/identity) describes
the modes in full.

This stack is for local use only. It uses fixed passwords, the password grant, a plaintext
Keycloak endpoint, and it skips Trino certificate verification. A real deployment uses managed
secrets, a proper login flow, and verified TLS.

Here is what happens when you run a query against the passthrough release:

```
you  ──(1) log in──────────────►  Keycloak      returns a token that names you
you  ──(2) query + your token──►  server        checks the token, then plans the query
                 server ──(3) run as you──────►  Trino         applies its per-user rules
```

### Two identities reach the engine

Trino accepts two kinds of connection at once, each carrying a different identity:

- **The caller.** Your query carries a Keycloak token. In passthrough and exchange, Trino
  reads the `preferred_username` claim and runs the query as that user, so Trino's own
  per-user rules apply. This is why alice and bob get different answers.
- **The operator.** The operator connects to Trino with its own service credentials over Basic
  auth, not with a user token. The manager uses one credential for reading metadata and
  managing views, and the server uses a separate credential for queries. In static mode the
  server runs every query under that server credential. TLS is on because Trino only accepts
  Basic auth over HTTPS.

These two identities stay separate by design, so the operator's own access does not widen
what a caller sees. What the engine returns depends on the caller's identity.

## The engine's policy

Trino file rules on the `tpch` catalog decide what each user may see:

- `bob` is denied `SELECT` on `tpch.tiny.customer`.
- `bob` sees `tpch.tiny.orders.clerk` masked as `REDACTED`.
- `alice` and `carol` have full access.

These are engine-side rules. They apply only under `passthrough` and `exchange`, where the
query runs as the caller. Under `static` the query runs as the server, so every caller sees
the same result.

## The identities

Keycloak realm `semantic`, client `semantic-cli`, direct access password grant. The users
are `alice`, `bob`, and `carol`, all with password `password`.

## Open port-forwards

The services run inside the cluster, so forward the two you need to your machine. Keycloak
serves HTTP on 8080, and each mode's server serves 8090 in its own namespace. Forward Keycloak
and the passthrough server to start:

```bash
kubectl -n semantic-system port-forward service/keycloak 18080:8080 &
kubectl -n sem-passthrough port-forward service/semantic-operator-server 19002:8090 &
sleep 3
```

## Mint a token

Get an access token for alice from the realm's token endpoint:

```bash
TOKEN=$(curl -s http://127.0.0.1:18080/realms/semantic/protocol/openid-connect/token \
  -d client_id=semantic-cli -d grant_type=password \
  -d username=alice -d password=password | jq -r .access_token)
```

## Read the claims

The token is a JWT. Decode the payload to see who the server will trust and what identity
the engine will run under:

```bash
echo "$TOKEN" | jq -R 'split(".")[1] | @base64d | fromjson
  | {iss, aud, preferred_username, semantic_roles}'
```

The server verifies the token signature against the realm JWKS, checks that `iss` matches the
configured issuer, and checks that `aud` includes the audience it requires. This token carries
several audiences, including one for the semantic API and one for Trino, which is why Trino
also accepts it under passthrough. `preferred_username` is the claim Trino uses as the session
user. In `jwt` mode the server never trusts an identity header, so this token is the whole
identity.

## Call the server as alice and bob

Send the token to the passthrough server. Ask for a metric and the allowed `orders.clerk`
dimension:

```bash
curl -s -X POST http://127.0.0.1:19002/v1/models/tpch_orders_model/query \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"metrics":["total_price"],"dimensions":["orders.clerk"],"limit":1}' | jq
```

alice sees a real clerk name. Now mint bob's token and run the same request:

```bash
TOKEN=$(curl -s http://127.0.0.1:18080/realms/semantic/protocol/openid-connect/token \
  -d client_id=semantic-cli -d grant_type=password \
  -d username=bob -d password=password | jq -r .access_token)

curl -s -X POST http://127.0.0.1:19002/v1/models/tpch_orders_model/query \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"metrics":["total_price"],"dimensions":["orders.clerk"],"limit":1}' | jq
```

bob sees `REDACTED` in the clerk column. Nothing changed but the token, so the different
result comes from Trino masking the column for bob.

Now ask bob for the denied table through the `customer.mktsegment` dimension:

```bash
curl -s -X POST http://127.0.0.1:19002/v1/models/tpch_orders_model/query \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"metrics":["total_price"],"dimensions":["customer.mktsegment"],"limit":1}' | jq
```

The response carries an access-denied error, not rows. Trino refused the read under bob's
identity. alice's token on the same request returns rows.

## A request with no token

A request with no bearer token never reaches the engine. The server rejects it:

```bash
curl -s -o /dev/null -w '%{http_code}\n' \
  -X POST http://127.0.0.1:19002/v1/models/tpch_orders_model/query \
  -H 'Content-Type: application/json' \
  -d '{"metrics":["total_price"],"limit":1}'
# 401
```

## Compare the modes

Forward the other two servers and send the same query to each:

```bash
kubectl -n sem-static   port-forward service/semantic-operator-server 19001:8090 &
kubectl -n sem-exchange port-forward service/semantic-operator-server 19003:8090 &
sleep 3
```

Against `sem-static` on 19001, bob and alice get the same result, because every query runs
as the server's own engine credential. Against `sem-exchange` on 19003, bob is masked and
denied just as in passthrough, because the server exchanges the caller token for an
engine-audience token under the same subject. [Identity and the engine](/architecture/identity)
explains the exchange flow.

## Next

[Identity and the engine](/architecture/identity) covers the three modes and the token
exchange. [The semantic server](/architecture/server) describes the full request pipeline.
[Access and credentials](/architecture/access) sets out what each component is granted.
