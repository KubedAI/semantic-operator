# Glue + Trino on EKS

The same model served by Trino instead of StarRocks.

**Full documentation: https://kubedai.github.io/semantic-operator/examples/glue-trino**

The files in this directory are the runnable parts. The instructions that go with
them live on the docs site so they stay in one place.

| File | Purpose |
|---|---|
| `data-load.sh` | Builds the retail tables in a Glue-backed Iceberg schema from Trino's built-in `tpcds`. Self-contained, needs no existing warehouse. |
| `semanticmodel.yaml` | The certified model, with `viewDatabase` catalog qualified for Trino. |

Both are idempotent and print their own verification. Every service is reached
with `kubectl port-forward`, and nothing here creates a load balancer.
