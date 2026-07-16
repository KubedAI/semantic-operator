# Examples

Each example is a self-contained use case with its own README. Examples are
grouped by query engine, so additional engines (ClickHouse, Trino, DuckDB — see
[docs/EXTENDING-ENGINES.md](../docs/EXTENDING-ENGINES.md)) slot in as new
top-level directories without disturbing the existing ones.

```
examples/
  starrocks/
    retail/     # runnable end-to-end: TPC-DS subset, loader, model, NL demo, benchmark, Superset
    flights/    # model-only: a second Glue-bound domain, shows authoring + binding
```

| Example | Engine | Kind | Start here |
|---|---|---|---|
| [starrocks/retail](starrocks/retail/README.md) | StarRocks | Runnable end-to-end (data loader included) | **Yes — the reference example** |
| [starrocks/flights](starrocks/flights/README.md) | StarRocks | Model-only (authoring + Glue binding) | After retail |

## How examples relate to the rest of the repo

- The **operator** and **server** are engine- and example-agnostic; you install
  them once ([docs/DEVELOPER.md → Deploy & operate](../docs/DEVELOPER.md#deploy--operate)).
- Each example supplies only its **`SemanticModel` CR** (and, for retail, a data
  loader and demo tooling). Applying the CR is all it takes to serve that model.
- The engine-specific bits an example depends on (a StarRocks external catalog,
  Glue databases) are called out in that example's README.

New use cases should add a directory under the appropriate engine and a README
following the retail example's shape.
