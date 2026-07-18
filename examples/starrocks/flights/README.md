# Flights example (Apache Ossie → semantic_model, Glue-bound)

A second, distinct-domain example that shows how an Apache Ossie model binds to
physical tables in AWS Glue through this operator. Where the retail example
(`examples/starrocks/retail/`) is a full runnable end-to-end demo with a data
loader, this one is **model-only**: it is the semantic model itself, meant to
show authoring and Glue binding.

## Provenance

Adapted from the upstream Apache Ossie flights example:
<https://github.com/apache/ossie/blob/main/examples/flights.yaml>.

That file uses Ossie's **`ontology` / `ontology_mappings`** representation. This
operator implements the **`semantic_model`** form (`osi.datasets` /
`relationships` / `metrics`), so `semanticmodel.yaml` here is a faithful
re-expression of the same flights domain in the form our validator and planner
consume. (Ontology-form support is intentionally out of scope; see
`docs/ARCHITECTURE.md`.)

## How it binds to Glue

`spec.connection` pins the physical location:

```yaml
connection:
  catalog: iceberg          # the StarRocks external catalog fronting Glue
  database: osi_flights     # a Glue database
```

Each `dataset.source` is a bare table name that the operator resolves against
`spec.connection` to `iceberg.osi_flights.<table>`. So `source: flights` binds
to `iceberg.osi_flights.flights`, backed by an Iceberg table registered in the
Glue Data Catalog. A fully-qualified `source` (`db.schema.table`) is used as-is;
this keeps the Ossie document portable while the CR pins the binding.

**Same table, two roles.** `orig_airport` and `dest_airport` are two datasets
that both set `source: airports` — they bind to the *same* physical Glue table
but are aliased by their dataset name in the emitted SQL
(`airports AS orig_airport`, `airports AS dest_airport`). This is the
star-schema pattern the ontology form expresses as two link mappings to the
`Airport` entity, and it is how you join origin and destination against one
airports table.

## Try it

Offline validation (no cluster needed):

```bash
go run ./cmd/osictl validate -f examples/starrocks/flights/semanticmodel.yaml
# OK: ... (model flights_model, version <hash>)
```

Derive a full Ossie model scaffold from your own Glue flights database instead
of hand-writing fields (datasets and fields are populated from the catalog;
metrics, relationships, synonyms, and governance are emitted as `TODO`
placeholders to fill in):

```bash
go run ./cmd/osictl derive -region us-west-2 -database osi_flights -out flights-derived.yaml
# writes to stdout if -out is omitted
```

To run it end to end, create the `osi_flights` Glue database and the
`flights / carriers / routes / airports` Iceberg tables (via StarRocks INSERTs,
Spark, or your own pipeline), then `kubectl apply -f semanticmodel.yaml` and
query it exactly like the retail example (see
[`../retail/README.md`](../retail/README.md)). A loader is not included for this
example.

## What it defines

- **Datasets:** `flights` (fact), `carriers`, `routes`, `orig_airport`,
  `dest_airport` (last two bound to the same `airports` table).
- **Metrics:** `flight_count`, `avg_dep_delay`, `avg_arr_delay`,
  `cancelled_flights`, `cancellation_rate` (a fan-out-safe ratio over the
  flights fact), `avg_distance`.
- **Governance:** the `analyst` role is denied precise airport coordinates
  (`a_latitude` / `a_longitude`); `carrier_analyst` is additionally row-filtered
  to a single carrier; `admin` sees everything.
- **Views:** `delays_by_carrier`, `flights_by_origin_state`, `monthly_flights`.
