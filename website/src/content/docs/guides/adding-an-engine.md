---
title: Adding a query engine
description: An engine is a SQL dialect plus a connection client. Both register themselves by name, and one setting selects the pair. Trino shipped this way and is the worked example.
---

Supporting a new engine means writing two small pieces and registering them. The compiler
does not change, and neither does the operator, the governance model, or the serving
layer.

Trino was added exactly this way. Its dialect is about 70 lines and its client about 150.
Diff those two files against the StarRocks equivalents and you have seen the whole job.

## The two halves

An engine is split into the part that writes SQL text and the part that carries it to the
database.

A **dialect** renders the engine specific atoms. Identifier quoting, literal syntax, date
truncation, null safe equality, and the DDL that creates a schema. It knows nothing about
connections.

A **client** runs statements and introspects schemas. Query, execute, ping, describe a
table, and close. It knows nothing about SQL semantics.

Keeping them apart is what lets an HTTP engine such as Trino and a MySQL protocol engine
such as StarRocks share the same compiler.

## Step 1. Write the dialect

Create `internal/emitter/<engine>/` and implement the six methods. Register the dialect
from `init()` so a blank import is all the wiring it needs.

```go
func init() { emitter.Register(Dialect{}) }

func (Dialect) QuoteIdent(ident string) string {
    return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}
```

The places engines actually differ are worth knowing in advance.

| Atom | StarRocks | Trino |
|---|---|---|
| Identifier quoting | Backticks | Double quotes |
| String escaping | Backslash is an escape | Backslash is an ordinary character |
| Timestamp literal | Plain quoted string | `TIMESTAMP '...'` |
| Null safe equality | `<=>` | `IS NOT DISTINCT FROM` |
| Creating a schema | `CREATE DATABASE` | `CREATE SCHEMA` |

Trino is strict about types, so emit typed literals for time values rather than relying on
an implicit cast. Week boundaries also differ between engines, so check the convention
before promising that a weekly grain matches another engine.

Add a table driven test next to the dialect that pins the emitted SQL.

## Step 2. Write the client

Create `internal/<engine>/client.go` implementing the connection interface, and register a
factory from `init()`.

```go
func init() {
    dbclient.Register("myengine", func(cfg dbclient.Config) (dbclient.Client, error) {
        return Open(cfg)
    })
}
```

`Query` and `Exec` can usually be copied from an existing client. Converting byte slices to
strings in `Query` keeps results friendly to JSON encoding and is worth keeping.

Only `DescribeTable` needs real thought, and two rules matter.

**A missing table must return an error, never an empty column list.** The reconciler uses
that distinction to tell drift apart from an empty table.

**Verify `information_schema` on a live system before trusting it.** Trino's Iceberg
connector omits tables it cannot load, while `SHOW TABLES` still lists them. The operator
correctly reports that as drift, but it is confusing the first time you meet it.

Apply your engine's defaults inside `Open` for any zero valued config, such as the default
port.

## Step 3. Register it

Add one blank import for each package to both `cmd/manager/main.go` and
`cmd/server/main.go`.

```go
_ "github.com/KubedAI/semantic-operator/internal/emitter/myengine"
_ "github.com/KubedAI/semantic-operator/internal/myengine"
```

That is the entire wiring. At runtime the `engine.type` Helm value selects both halves, and
connection settings come from the `ENGINE_*` environment variables.

## Step 4. Prove it

The bar for saying an engine works is that the same model produces the same numbers as an
existing engine.

```bash
go test ./internal/emitter/myengine/ ./internal/myengine/
go test ./internal/planner/          # cross dialect regression tests
```

Then deploy against a real instance, apply the retail model, and compare a fan out safe
ratio against the published result. That is how Trino was validated, and it caught real
bugs that unit tests did not.

## A note on credentials

The connection config carries a host, port, user, and password today. Engines that expect a
short lived credential minted from an IAM role, such as Amazon Redshift, need a credential
provider seam that does not exist yet. If you are adding one of those, raise it first so
the interface changes once rather than per engine.
