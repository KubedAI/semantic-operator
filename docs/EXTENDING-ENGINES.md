# Adding a query engine (Trino, ClickHouse, DuckDB)

This guide shows how to teach the semantic layer to emit and run SQL against a new engine. It covers the **Tier 1** engines: Trino/Presto, ClickHouse, and DuckDB. These have full SQL, multi-table joins, `CREATE VIEW`, and a `date_trunc`-style function. For those, adding support means implementing two small things and wiring them up. No planner changes.

> **You do not touch Iceberg here.** The planner only ever emits `catalog.database.table` references and hands the SQL to the engine. Whether those tables are Iceberg, Delta, Hive, or engine-native is the engine's external-catalog concern, invisible to this code. If your engine points at the same Glue and Iceberg catalog StarRocks uses (Trino especially), the same lake tables resolve with zero extra work. See [ARCHITECTURE.md](ARCHITECTURE.md#extension-points).

## The two seams you implement

The planner is a compiler. It builds a logical plan and renders it through interfaces. To add an engine you provide two things.

1. **An `emitter.Dialect`.** The engine-specific SQL atoms (quoting, literals, `DATE_TRUNC`, null-safe equality). About 40 lines. See `internal/emitter/starrocks/starrocks.go` for the reference.
2. **A query client.** A thin `database/sql` wrapper that satisfies the three narrow runtime interfaces the rest of the system already depends on. About 80 lines, largely a copy of `internal/starrocks/client.go`.

Those three runtime interfaces already exist. Nothing consumes the concrete `starrocks.Client` type except the two `main.go` wiring points.

| Interface | Method | Used by |
|---|---|---|
| `serving.QueryExecutor` | `Query(ctx, sql) ([]string, [][]any, error)` | MCP and REST query path |
| `views.Executor` | `Exec(ctx, sql) error` | governed-view publisher |
| `controllers.StarRocksClient` | `DescribeTable(ctx, catalog, db, table) ([]starrocks.Column, error)` | drift-check in the reconciler |

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

	"github.com/vara-bonthu/osi-semantic-operator/internal/emitter"
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

`Query` and `Exec` can be copied verbatim from the StarRocks client. The `[]byte` to `string` normalization in `Query` keeps results JSON-friendly and is worth keeping. Only `DescribeTable` needs an engine-specific query. Prefer the portable `information_schema.columns`, which all three support.

```go
func (c *Client) DescribeTable(ctx context.Context, catalog, database, table string) ([]starrocks.Column, error) {
	// information_schema is portable across Trino, ClickHouse, and DuckDB.
	q := fmt.Sprintf(
		"SELECT column_name, data_type FROM information_schema.columns "+
			"WHERE table_catalog = %s AND table_schema = %s AND table_name = %s "+
			"ORDER BY ordinal_position",
		lit(catalog), lit(database), lit(table)) // lit() = your dialect's string literal
	cols, rows, err := c.Query(ctx, q)
	// ...map rows -> []starrocks.Column{Name, Type}
}
```

Engine-native alternatives if you prefer. Trino has `DESCRIBE cat.db.tbl` or `SHOW COLUMNS FROM cat.db.tbl`. ClickHouse has `DESCRIBE TABLE db.tbl`. DuckDB has `DESCRIBE db.tbl`. ClickHouse has no `table_catalog`, so filter on `table_schema` and `table_name` only.

> **Note on the return type.** `controllers.StarRocksClient.DescribeTable` returns `[]starrocks.Column`, a plain `{Name, Type string}` struct. The quickest path is to import that struct and return it. For a cleaner contribution, move `Column` into a neutral package (for example `internal/catalog`) and update the controller interface. That is a small, self-contained refactor. Either is acceptable.

---

## Step 3. Wire it up

Two spots construct the concrete client today: `cmd/server/main.go` and `cmd/manager/main.go`. Both call `starrocks.Open(...)` directly. Generalize the selection with the `SQL_DIALECT` env var that already drives the emitter, so one variable picks both the dialect and the client.

Add a tiny factory (for example `internal/dbclient/factory.go`) returning a value that satisfies `Query`, `Exec`, and `DescribeTable`.

```go
func Open(ctx context.Context, dialect string, cfg Config) (DB, error) {
	switch dialect {
	case "starrocks":
		return starrocks.Open(starrocks.Config{ /* ... */ })
	case "trino":
		return trino.Open(trino.Config{ /* ... */ })
	// clickhouse, duckdb ...
	default:
		return nil, fmt.Errorf("no DB client for dialect %q", dialect)
	}
}
```

Then in `cmd/server/main.go`, replace the hardcoded open with the factory driven by the same variable used for the emitter (`emitter.Get(envOr("SQL_DIALECT", "starrocks"))` is already there at `cmd/server/main.go:46`).

```go
dialectName := envOr("SQL_DIALECT", "starrocks")
dialect, err := emitter.Get(dialectName)
// ...
db, err := dbclient.Open(ctx, dialectName, cfg)
// svc.DB = db ; readiness Ping = db.Ping
```

Register the dialect with a blank import next to the existing StarRocks one (`cmd/server/main.go:20`).

```go
_ "github.com/vara-bonthu/osi-semantic-operator/internal/emitter/starrocks"
_ "github.com/vara-bonthu/osi-semantic-operator/internal/emitter/trino"
```

Do the same in `cmd/manager/main.go` for the reconciler's `StarRocks` field, the drift-check client.

---

## What you get for free

- **Governed views.** All three engines support `CREATE VIEW`, so the view publisher (`internal/serving/views`) works unchanged once the dialect emits valid `CREATE OR REPLACE VIEW` DDL. Trino view support depends on the connector. The Iceberg and Hive connectors support it.
- **Governance.** Row and column policies are applied inside the planner before SQL is emitted, independent of engine. Nothing to port.
- **Determinism, caching, MCP and REST, drift-check.** All engine-agnostic. They sit above the two seams.
- **Iceberg.** Nothing to do. If the engine reads the same Glue and Iceberg catalog (Trino's Iceberg and Glue connector reads the exact tables StarRocks does), the lake comes along automatically.

## Optional: catalog derivation for `osictl`

`osictl` can derive dataset stubs from a live catalog via `catalog.Source` (`ListTables`). The shipped implementation is Glue. Trino, ClickHouse, and DuckDB pointed at Glue and Iceberg can reuse the Glue source as is. For an engine-portable alternative, implement a `catalog.Source` over `information_schema.columns` (the same query as `DescribeTable`, scoped to a schema) so derivation works without Glue. This is optional. Models can also be authored by hand.

## Checklist

- [ ] `internal/emitter/<engine>/<engine>.go` implementing `emitter.Dialect` and the `init()` register
- [ ] Dialect golden-SQL test
- [ ] `internal/<engine>/client.go` with `Query`, `Exec`, `DescribeTable`
- [ ] A `SQL_DIALECT`-driven client factory, and update both `main.go` wirings and blank imports
- [ ] (Optional) an `information_schema` `catalog.Source` for `osictl` derivation
- [ ] Add `<engine>` to the `DialectExpression` enum in `api/v1alpha1/semanticmodel_types.go` if models will carry engine-specific expressions, then `make generate`
- [ ] Helm: surface the engine's connection env vars in `charts/semantic-operator/values.yaml`

Scope guardrail: keep every engine-specific line behind `emitter.Dialect` and the query client. If you find yourself special-casing an engine inside the planner, that belongs in the dialect instead. Or it is a Tier 2 concern (real-time OLAP engines without joins or views), which is out of scope here.
