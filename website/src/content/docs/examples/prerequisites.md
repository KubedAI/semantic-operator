---
title: Prerequisites
description: What you need before running any example, including tools, cluster access, and the one time setup each deployment stack expects.
---

Every example assumes the same starting point. Set this up once and the walkthroughs will
run straight through.

## On your machine

| Tool | Why |
|---|---|
| `kubectl` | Applying models and reaching services through port forwards |
| `helm` | Installing the operator and server |
| Go 1.26 or later | Running `ossiectl` for model generation and validation |
| `jq` | Reading the JSON responses in the verification steps |
| A MySQL client | Only for StarRocks stacks, to run catalog setup and read governed views |
| `aws` CLI | Only for stacks that use AWS Glue or S3 |

Check your context points at the right cluster before you begin.

```bash
kubectl config current-context
```

## A Kubernetes cluster

Any cluster works. The examples were written and verified on Amazon EKS, and the
[Data on EKS](https://awslabs.github.io/data-on-eks/) blueprints are a quick way to get a
cluster with a query engine already running.

You need enough room for the operator and server, which are small, plus whichever engine
and catalog the example uses, which are not. The local laptop stack is the exception and
manages its own resources.

## A query engine

One of the following, reachable from the cluster.

**StarRocks** in shared data mode, with a front end that speaks the MySQL protocol. This is
the reference engine.

**Trino** with a coordinator reachable over HTTP.

The engine does not have to run in the same cluster as the operator. It only has to be
reachable at a host and port, with credentials.

## Container images

Published on Amazon ECR Public, multi-arch for `linux/amd64` and `linux/arm64`.
No registry setup and no build.

```
public.ecr.aws/data-on-eks/semantic-operator/manager:v0.1.1
public.ecr.aws/data-on-eks/semantic-operator/server:v0.1.1
```

Pass the base path and the tag to Helm.

```bash
--set image.repository=public.ecr.aws/data-on-eks/semantic-operator --set image.tag=v0.1.1
```

To run a build of your own instead, push to any registry your cluster can pull from.

```bash
make docker-build docker-push REGISTRY=<your-registry> TAG=<tag>
```

For Amazon ECR, authenticate first and create the repositories once.

```bash
make ecr-create ecr-login REGISTRY=<acct>.dkr.ecr.<region>.amazonaws.com
```

Use the same `TAG` value in the Helm install. A tag mismatch is the most common reason a
first install sits in `ImagePullBackOff`.

## Database credentials

The operator and the server want different database users, because they need different
rights. The operator reads metadata and creates views. The server only reads data.

| Component | Grants it needs |
|---|---|
| Operator | Read metadata on the model schemas, plus create and drop views in one schema |
| Server | `SELECT` on the schemas the model binds |

You can start with a single user for both while trying things out, then split them with
`engine.manager.*` and `engine.server.*` before anyone relies on it. See
[Access and credentials](/architecture/access) for the full matrix.

## How you will reach the services

Every example uses `kubectl port-forward`. The server's Service is `ClusterIP` and the
chart refuses to publish it externally without an explicit override, because a load
balancer in front of a server that trusts a role header is an unauthenticated query
endpoint.

```bash
kubectl -n semantic-system port-forward svc/semantic-operator-server 8090:8090
```

If a port forward stops responding partway through a walkthrough, kill it and start it
again. It is the most common cause of a confusing failure.

```bash
pkill -f 'kubectl.*port-forward'
```

## Next

Pick a stack from [Choosing an example](/examples), or go straight to
[Retail on Glue and StarRocks](/examples/glue-starrocks), which is the reference
walkthrough.
