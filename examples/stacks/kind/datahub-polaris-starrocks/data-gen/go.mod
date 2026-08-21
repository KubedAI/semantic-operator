// Standalone, self-contained generator/loader for the local saas-accounts
// demo dataset. It is deliberately its own Go module (not part of the parent
// demo module) so it stays lightweight: one dependency, no AWS/S3/Glue code,
// no profiles, no manifest, no force/reset. It generates the fixed small
// (~44k-row) dataset and loads it into the local StarRocks Iceberg catalog.
module account-demo.local/datagen

go 1.26.2

require github.com/go-sql-driver/mysql v1.10.0

require filippo.io/edwards25519 v1.2.0 // indirect
