# data-gen — standalone dataset generator for the local demo

A small, self-contained Go module that deterministically generates the
customer-health demo dataset and loads it into the local StarRocks **Iceberg**
external catalog. It is intended for the **local** (kind) deployment only.

It is deliberately lightweight: its own `go.mod`, one dependency (the MySQL
driver StarRocks speaks), and **none** of the parent demo's operational
scaffolding — no AWS/S3, no Glue, no IAM preflight, no storage-location
management, no data profiles, no `_demo_manifest`/checksums, and no
force/reset. The REST catalog (Polaris) owns table locations, so the loader
just runs `CREATE TABLE IF NOT EXISTS` and batched `INSERT`s, then verifies each
table's row count.

## Dataset

Eleven business tables, ~44,835 rows total (fact volume scales with a
60-account base):

| Table | Rows | | Table | Rows |
|---|--:|---|---|--:|
| `date_dim` | 1,095 | | `account_feature_entitlement` | 360 |
| `account` | 60 | | `subscription_monthly` | 1,680 |
| `account_primary_contact` | 60 | | `usage_daily` | 36,000 |
| `plan` | 12 | | `support_ticket` | 4,000 |
| `contract` | 80 | | `account_feature_monthly` | 1,440 |
| `product_feature` | 48 | | | |

Generation is deterministic (fixed seed `20260720`); each table has an
independent random stream, and the point-in-time `current_*` projection columns
the SemanticModels depend on are computed exactly as in the reference dataset.
The fixed **Northstar Systems** anchor (account `1001`, NA/Enterprise,
renewal `2026-09-15`, low adoption, elevated support) is preserved so the demo's
metrics stay meaningful. Invariants (e.g. every `usage_daily`
`(account_id, feature_id)` has exactly one entitlement) are enforced during
generation — a violation aborts with an error rather than loading bad data.

## Usage

Normally run via the demo Makefile / script, which targets the FE NodePort:

```bash
make data-load          # from local/
# or:
scripts/data-load.sh
```

Directly:

```bash
cd local/data-gen
go run . --host 127.0.0.1 --port 9030 --user root --catalog iceberg
```

Flags: `--host` `--port` (default 9030) `--user` (root) `--password`
`--catalog` (iceberg) `--database` (saas_customer_health_demo)
`--batch-size` (1000).

Offline self-check — generate everything and print row counts without
connecting to a database:

```bash
go run . --count-only
```

## Layout

```
main.go            flags, StarRocks connection, --count-only self-check
constants/         seed, window dates, and the fixed small-profile row counts
gen/               deterministic streaming generators (dims + facts + anchors)
loader/            catalog-owned CREATE TABLE + batched INSERT + row-count verify
```
