# DataHub + Polaris + Trino on EKS

Staged walkthrough: Polaris as the Iceberg REST catalog, Trino as the engine, DataHub for business metadata.

**Full documentation: https://kubedai.github.io/semantic-operator/examples/datahub-polaris-trino**

The files in this directory are the runnable parts. The instructions that go with
them live on the docs site so they stay in one place.

## What each script does

| Script | Stage | Notes |
|---|---|---|
| `eks-up.sh` | 1 | Postgres, Polaris, catalog `demo`. Skip if your stack already runs Polaris. |
| `trino-catalog.sh` | 2 | Adds the `polaris` catalog to Trino. Skip if it is already there. |
| `data-load.sh` | 3 | Builds the retail tables from Trino's built-in `tpcds`. Self-contained, needs no other cluster or bucket. |
| `datahub-ingest.sh` | 5 | Ingests the Polaris tables into DataHub as an in-cluster Job. |
| `datahub-annotate.sh` | 5 | Adds glossary terms, descriptions, and the PII tag a steward would own. |
| `semanticmodel.yaml` | 7 | The certified model. Metrics and access rules are human written. |

Every script is idempotent and prints its own verification. Services are
reached over `kubectl port-forward` only, and nothing here creates a load
balancer.
