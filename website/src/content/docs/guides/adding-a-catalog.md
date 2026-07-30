---
title: Adding a catalog source
description: Catalogs supply physical structure and business meaning for model generation. Neither is used at runtime, which is why the operator needs no cloud credentials.
---

Catalog integrations serve optional offline model generation only. Nothing in the runtime
path touches them. The normal connected authoring path also introspects through the query
engine. Every catalog an engine can mount already works with no catalog-specific code.

That separation is worth holding onto. It is why the operator needs no cloud credentials.

## Two different jobs

There are two interfaces because there are two genuinely different questions.

A **source** answers "what tables and columns exist". It has to be authoritative about the
current schema, so implementations read either the catalog the engine itself reads or the
engine's own `information_schema`.

An **enricher** answers "what does this mean". Descriptions, business vocabulary, and
sensitivity classifications. Metadata platforms hold this but serve an ingested copy of the
schema that can lag reality, so they decorate a scaffold rather than define one.

Getting this the wrong way round produces models that fail their first drift check.

## Sources that already ship

Before writing a new source, check whether you need one.

**infoschema** reads the engine's own `information_schema`. Because the engine already
mounts the physical catalog, this single implementation covers Polaris and other Iceberg
REST catalogs, Hive Metastore, Unity, and Glue, with no catalog specific code. It also sees
exactly what the engine sees, which is the same property drift detection relies on.

**Glue** reads AWS Glue directly over the SDK, with no engine or cluster involved. It is
the example native source for offline bootstrap.

Prefer `infoschema` when an engine is available. A native source is only worth writing
when users need to generate a model without a running engine or need metadata the engine
does not expose.

## Writing a source

Implement one method that lists tables with their columns.

```go
type Source interface {
    ListTables(ctx context.Context, database string) ([]Table, error)
}
```

Return columns in a stable order, ideally the catalog's own ordinal order, so regenerating
a model produces a clean diff rather than a reshuffle.

## Writing an enricher

Implement one method that returns metadata keyed by table name.

```go
type Enricher interface {
    DescribeTables(ctx context.Context, database string, tables []string) (map[string]TableMeta, error)
}
```

Enrichment is additive and best effort. An unknown table or column yields no entry rather
than an error, because a scaffold without enrichment is still valid and useful.

What the DataHub implementation maps, as a guide for others.

| Upstream | Becomes | Note |
|---|---|---|
| Descriptions | `description` | Curated edits win over ingested text |
| Glossary terms | `ai_context.synonyms` | How an agent grounds a person's words |
| PII and sensitivity tags | `governance.denyFields` | The field then fails to compile |
| Deprecation | A review comment | Flagged for a person, never dropped silently |

Metrics are deliberately never imported. Certifying a formula stays a human decision.

Two things the DataHub work learned the hard way, both likely to apply elsewhere.

**Metadata platforms often keep curated edits separately from ingested metadata.** DataHub
writes steward edits to different aspects entirely, so reading only the ingested layer
misses everything a person actually curated. Read both and let the curated layer win.

**A classification on a column that no longer exists must be ignored.** Otherwise
enrichment generates a policy referencing a missing field and the model fails validation.
Walk the physical columns, not the metadata.

## Wiring it up

Sources and enrichers are selected by flag in the authoring tool rather than registered
globally, because they run in a person's shell rather than in the cluster. Add your case to
the `-source` or `-enrich` switch in `cmd/ossiectl`, and document the flags it needs.

There is no operator or server change. That is the point.
