# Adding a query engine (ClickHouse, DuckDB, Redshift, ...)

This guide shows how to teach the semantic layer to emit and run SQL against a new engine. It targets **Tier 1** engines: those with full SQL, multi-table joins, `CREATE VIEW`, and a `date_trunc`-style function. Adding one means implementing two small things and registering them. No planner changes.

**Trino shipped this way** and is the worked example throughout: `internal/emitter/trino/trino.go` (the dialect, ~70 lines) and `internal/trino/client.go` (the client, ~150 lines), both registered by name and selected together by the `SQL_DIALECT` env var (Helm value `engine.type`). Diff those two files against their StarRocks counterparts to see exactly what an engine port touches.

> **You do not touch Iceberg here.** The planner only ever emits `catalog.database.table` references and hands the SQL to the engine. Whether those tables are Iceberg, Delta, Hive, or engine-native is the engine's external-catalog concern, invisible to this code. If your engine points at the same Glue and Iceberg catalog StarRocks uses (Trino especially), the same lake tables resolve with zero extra work. See [ARCHITECTURE.md](ARCHITECTURE.md#extension-points).

## The two seams you implement

The planner is a compiler. It builds a logical plan and renders it through interfaces. To add an engine you provide two things.

1. **An `emitter.Dialect`.** The engine-specific SQL atoms (quoting, literals, `DATE_TRUNC`, null-safe equality, schema-creation DDL). About 40–70 lines. References: `internal/emitter/starrocks/` (MySQL family) and `internal/emitter/trino/` (double-quote ANSI family).
2. **A `dbclient.Client`.** A thin `database/sql` wrapper implementing `Query`, `Exec`, `Ping`, `DescribeTable`, and `Close`. References: `internal/starrocks/client.go` and `internal/trino/client.go`.

Both register themselves by engine name from `init()` (`emitter.Register`, `dbclient.Register`) and the binaries pick them up through one blank import each in `cmd/manager` and `cmd/server`. `SQL_DIALECT` selects the pair at runtime; there is no other wiring.

The narrow consumer interfaces the rest of the system depends on:

| Interface | Method | Used by |
|---|---|---|
| `serving.QueryExecutor` | `Query(ctx, sql) ([]string, [][]any, error)` | MCP and REST query path |
| `views.Executor` | `Exec(ctx, sql) error` | governed-view publisher |
| `controllers.EngineClient` | `DescribeTable(ctx, catalog, db, table) ([]dbclient.Column, error)` | drift-check in the reconciler |

Two hard-won rules for `DescribeTable`: a missing table must return an **error**, never an empty column list, so the reconciler can tell drift from an empty table; and do not blindly trust `information_schema` — Trino's Iceberg connector silently omits tables it cannot load (for example when the engine lacks S3 access to the table's metadata), while `SHOW TABLES` still lists them. The operator surfaces that as per-dataset drift, which is exactly what you want, but it is worth knowing when debugging a new engine.

A single client type with `Query`, `Exec`, and `DescribeTable` methods satisfies all three.

---

## Step 1. Implement the dialect

Create `internal/emitter/<engine>/<engine>.go`, mirroring the StarRocks dialect. Register it in `init()` so `emitter.Get("<engine>")` resolves it.

```go
// Package trino implements the Trino/Presto SQL dialect.
package trino

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/KubedAI/semantic-operator/internal/emitter"
)

type Dialect struct{}

func init() { emitter.Register(Dialect{}) }

func (Dialect) Name() string { return "trino" }

func (Dialect) QuoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func (d Dialect) QualifyTable(catalog, database, table string) string {
	return d.QuoteIdent(catalog) + "." + d.QuoteIdent(database) + "." + d.QuoteIdent(table)
}

func (Dialect) DateTrunc(grain, scalar string) string {
	return "date_trunc('" + grain + "', " + scalar + ")"
}

func (Dialect) NullSafeEq(a, b string) string {
	return a + " IS NOT DISTINCT FROM " + b
}

// Literal — see the escaping/typing notes in the table below.
func (Dialect) Literal(v any) (string, error) { /* ... */ }
```

The six methods are the whole contract. Here is how each differs across the three engines. These are the only real per-engine deltas.

| Method | StarRocks (shipped) | Trino/Presto | ClickHouse | DuckDB |
|---|---|---|---|---|
| `QuoteIdent` | `` `ident` `` (backtick) | `"ident"` (double-quote) | `` `ident` `` (backtick) | `"ident"` (double-quote) |
| `QualifyTable` | `cat.db.tbl` (3-part) | `cat.db.tbl` (3-part, maps cleanly to StarRocks external catalogs) | `db.tbl` (2-part, ignore `catalog` or map it to the CH database) | `cat.db.tbl` or `db.tbl` (attached DB name as catalog) |
| `DateTrunc` | `DATE_TRUNC('month', x)` | `date_trunc('month', x)` | `date_trunc('month', x)` (recent CH, or `toStartOfMonth`) | `date_trunc('month', x)` |
| `NullSafeEq` | `a <=> b` | `a IS NOT DISTINCT FROM b` | `(a = b) OR (a IS NULL AND b IS NULL)` (no `IS NOT DISTINCT FROM`) | `a IS NOT DISTINCT FROM b` |
| `Literal` string | `'...'`, escape `\` and `'` | `'...'`, escape `'` by doubling (no backslash escaping) | `'...'`, escape `\` and `'` | `'...'`, escape `'` by doubling |
| `Literal` bool | `TRUE`/`FALSE` | `TRUE`/`FALSE` | `1`/`0` | `TRUE`/`FALSE` |
| `Literal` time | `'2006-01-02 15:04:05'` | `TIMESTAMP '2006-01-02 15:04:05'` (Trino needs typed literals) | `'2006-01-02 15:04:05'` | `TIMESTAMP '2006-01-02 15:04:05'` |

Two caveats worth a code comment.

- **Trino literal typing.** Trino is strict about types. A bare string compared to a `TIMESTAMP` column may not implicitly cast. Emit typed literals (`TIMESTAMP '...'`, `DATE '...'`) for time values.
- **`week` grain semantics** differ. The start-of-week day varies by engine. If a model uses `time_grain: week`, verify the engine's convention matches your expectation. Document it rather than assume parity with StarRocks.

Add a table-driven test next to the dialect (see `internal/planner/plan_test.go` for the golden-SQL style) so the emitted SQL is pinned.

---

## Step 2. Implement the query client

Create `internal/<engine>/client.go`. All three engines expose a `database/sql` driver, so this is almost a copy of `internal/starrocks/client.go`. Only the DSN and the `DescribeTable` query change.

Drivers:

| Engine | Go driver | `sql.Open` name |
|---|---|---|
| Trino/Presto | `github.com/trinodb/trino-go-client/trino` | `"trino"` |
| ClickHouse | `github.com/ClickHouse/clickhouse-go/v2` | `"clickhouse"` |
| DuckDB | `github.com/marcboeker/go-duckdb` | `"duckdb"` |

`Query` and `Exec` can be copied verbatim from the Trino client. The `[]byte` to `string` normalization in `Query` keeps results JSON-friendly and is worth keeping. Only `DescribeTable` needs an engine-specific query; it returns `[]dbclient.Column` (`{Name, Type string}`).

```go
func (c *Client) DescribeTable(ctx context.Context, catalog, database, table string) ([]dbclient.Column, error) {
	// information_schema is broadly portable; see internal/trino/client.go
	// for the full version with literal escaping and the empty-result check.
	q := describeQuery(catalog, database, table)
	cols, rows, err := c.Query(ctx, q)
	// ...map rows -> []dbclient.Column{Name, Type}
}
```

Engine-native alternatives if you prefer. ClickHouse has `DESCRIBE TABLE db.tbl` (and no `table_catalog` in information_schema, so filter on `table_schema` and `table_name` only). DuckDB has `DESCRIBE db.tbl`.

Two hard requirements, both learned the hard way (see the note at the top of this document):

1. A missing table must return an **error**, never an empty column list, so the reconciler reports drift instead of treating the table as empty.
2. Verify `information_schema` actually lists your tables on a live system before trusting it — Trino's Iceberg connector omits tables it cannot load while `SHOW TABLES` still names them.

---

## Step 3. Register it (there is no other wiring)

Both halves register themselves by name from `init()`; the factory already exists.

In your dialect package:

```go
func init() { emitter.Register(Dialect{}) }
```

In your client package:

```go
func init() {
	dbclient.Register("myengine", func(cfg dbclient.Config) (dbclient.Client, error) {
		return Open(cfg)
	})
}
```

Apply your engine's defaults inside `Open` for zero-valued `Config` fields (default port, default user) — see `internal/trino/client.go`.

Then add one blank import for each package to **both** `cmd/manager/main.go` and `cmd/server/main.go`, next to the existing ones:

```go
_ "github.com/KubedAI/semantic-operator/internal/emitter/myengine"
_ "github.com/KubedAI/semantic-operator/internal/myengine"
```

That is the entire wiring. At runtime, `SQL_DIALECT=myengine` (Helm value `engine.type`) selects both the dialect and the client; connection settings come from the `ENGINE_*` env vars via `dbclient.EnvConfig`. Finally, add your engine to the `values.yaml` comment for `engine.type` and, if it needs a non-standard connection scheme (TLS, tokens), extend `dbclient.Config` rather than inventing engine-specific env vars.

---

## What you get for free

- **Governed views.** All three engines support `CREATE VIEW`, so the view publisher (`internal/serving/views`) works unchanged once the dialect emits valid `CREATE OR REPLACE VIEW` DDL. Trino view support depends on the connector. The Iceberg and Hive connectors support it.
- **Governance.** Row and column policies are applied inside the planner before SQL is emitted, independent of engine. Nothing to port.
- **Determinism, caching, MCP and REST, drift-check.** All engine-agnostic. They sit above the two seams.
- **Iceberg.** Nothing to do. If the engine reads the same Glue and Iceberg catalog (Trino's Iceberg and Glue connector reads the exact tables StarRocks does), the lake comes along automatically.

## Optional: catalog derivation for `ossiectl`

`ossiectl` can derive dataset stubs from a live catalog via `catalog.Source` (`ListTables`). The shipped implementation is Glue. Trino, ClickHouse, and DuckDB pointed at Glue and Iceberg can reuse the Glue source as is. For an engine-portable alternative, implement a `catalog.Source` over `information_schema.columns` (the same query as `DescribeTable`, scoped to a schema) so derivation works without Glue. This is optional. Models can also be authored by hand.

## Checklist

- [ ] `internal/emitter/<engine>/<engine>.go` implementing `emitter.Dialect` and the `init()` register
- [ ] Dialect golden-SQL test
- [ ] `internal/<engine>/client.go` with `Query`, `Exec`, `DescribeTable`
- [ ] Register the dialect and client by name, and add both blank imports to `cmd/manager` and `cmd/server`
- [ ] (Optional) an `information_schema` `catalog.Source` for `ossiectl` derivation
- [ ] Add `<engine>` to the `DialectExpression` enum in `api/v1alpha1/semanticmodel_types.go` if models will carry engine-specific expressions, then `make generate`
- [ ] Helm: surface the engine's connection env vars in `charts/semantic-operator/values.yaml`

Scope guardrail: keep every engine-specific line behind `emitter.Dialect` and the query client. If you find yourself special-casing an engine inside the planner, that belongs in the dialect instead. Or it is a Tier 2 concern (real-time OLAP engines without joins or views), which is out of scope here.
