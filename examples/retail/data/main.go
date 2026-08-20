// This loader creates a deterministic, demo-sized TPC-DS subset by executing
// DDL and batched INSERTs through StarRocks or Trino. No Spark required.
//
// Idempotent: tables whose row counts already match are skipped. -force
// drops and reloads. -profile selects "full" (the default benchmark dataset)
// or "e2e" (a compact integration dataset). A fixed seed means the data, and
// therefore every benchmark ground-truth answer, is reproducible.
//
// Env (all optional except ENGINE_HOST or its legacy STARROCKS_HOST fallback):
//
//	SQL_DIALECT   (default "starrocks")   starrocks or trino
//	ENGINE_HOST, ENGINE_PORT, ENGINE_USER, ENGINE_PASSWORD
//	ENGINE_CATALOG (default ICEBERG_CATALOG, then "iceberg")
//	DEMO_DATABASE (default "osi_demo")
//
// The STARROCKS_* connection variables and ICEBERG_CATALOG remain supported
// for the existing StarRocks and Glue workflow.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/trino"
	_ "github.com/KubedAI/semantic-operator/internal/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/trino"
)

const seed = 20260702 // fixed: data and benchmark ground truth are reproducible

type dataProfile struct {
	dates, stores, items, customers, sales int
}

var (
	nDates     = 1096 // 2000-01-01 .. 2002-12-31
	nStores    = 12
	nItems     = 500
	nCustomers = 2000
	nSales     = 200_000
)

func selectDataProfile(name string) (dataProfile, error) {
	switch name {
	case "full":
		return dataProfile{dates: 1096, stores: 12, items: 500, customers: 2000, sales: 200_000}, nil
	case "e2e":
		return dataProfile{dates: 1096, stores: 6, items: 25, customers: 100, sales: 2_000}, nil
	default:
		return dataProfile{}, fmt.Errorf("unknown data profile %q (want full or e2e)", name)
	}
}

var categories = []string{"Books", "Electronics", "Home", "Jewelry", "Men", "Music", "Shoes", "Sports", "Children", "Women"}
var brandWords = []string{"amalg", "edu pack", "expor tuni", "impor tocorp", "scholar", "brandcorp", "corpna", "maxi", "univ", "nameless"}
var states = []string{"TX", "CA", "WA", "NY", "IL", "GA"}
var cities = []string{"Midway", "Fairview", "Oak Grove", "Bethel", "Pleasant Hill", "Centerville"}
var firstNames = []string{"James", "Mary", "Robert", "Patricia", "John", "Jennifer", "Michael", "Linda", "David", "Elizabeth", "Ana", "Wei", "Ravi", "Sofia", "Omar", "Yuki"}
var lastNames = []string{"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis", "Rodriguez", "Martinez", "Chen", "Patel", "Kim", "Nguyen", "Khan", "Lopez"}

func main() {
	force := flag.Bool("force", false, "drop and reload all tables")
	profileName := flag.String("profile", "full", "data volume profile: full or e2e")
	flag.Parse()

	profile, err := selectDataProfile(*profileName)
	if err != nil {
		log.Fatal(err)
	}
	nDates = profile.dates
	nStores = profile.stores
	nItems = profile.items
	nCustomers = profile.customers
	nSales = profile.sales

	engine := envOr("SQL_DIALECT", "starrocks")
	db := envOr("DEMO_DATABASE", "osi_demo")
	target, err := newSQLTarget(engine, loaderCatalog(), db)
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := dbclient.EnvConfig()
	if err != nil {
		log.Fatal(err)
	}
	cfg.QueryTimeout = 5 * time.Minute
	cli, err := dbclient.Open(engine, cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	if err := cli.Ping(ctx); err != nil {
		log.Fatalf("cannot reach %s at %s: %v", engine, cfg.Host, err)
	}

	// Catalog creation carries engine-specific configuration and remains an
	// administrator responsibility. The loader creates only its schema/tables.
	if _, _, err := cli.Query(ctx, dbclient.EngineCredential{}, target.catalogProbe()); err != nil {
		log.Fatalf("catalog %q is not usable through %s: %v", target.catalog, engine, err)
	}

	defer func() {
		if err := cli.Close(); err != nil {
			log.Printf("closing %s client: %v", engine, err)
		}
	}()

	must(cli.Exec(ctx, target.createSchema()))
	fq := target.table

	tables := []struct {
		name string
		ddl  string
		rows int
		load func(*loader)
	}{
		{"date_dim", `(
			d_date_sk INT, d_date DATE, d_year INT, d_moy INT,
			d_quarter_name VARCHAR(8), d_month_name VARCHAR(12), d_day_name VARCHAR(12))`,
			nDates, (*loader).dates},
		{"store", `(
			s_store_sk INT, s_store_id VARCHAR(16), s_store_name VARCHAR(50),
			s_city VARCHAR(60), s_state VARCHAR(2), s_number_employees INT)`,
			nStores, (*loader).stores},
		{"item", `(
			i_item_sk INT, i_item_id VARCHAR(16), i_item_desc VARCHAR(200),
			i_brand VARCHAR(50), i_category VARCHAR(50), i_current_price DECIMAL(7,2))`,
			nItems, (*loader).items},
		{"customer", `(
			c_customer_sk INT, c_customer_id VARCHAR(16), c_first_name VARCHAR(20),
			c_last_name VARCHAR(30), c_email_address VARCHAR(80), c_birth_year INT)`,
			nCustomers, (*loader).customers},
		{"store_sales", `(
			ss_sold_date_sk INT, ss_item_sk INT, ss_customer_sk INT, ss_store_sk INT,
			ss_ticket_number BIGINT, ss_quantity INT, ss_sales_price DECIMAL(7,2),
			ss_ext_sales_price DECIMAL(9,2), ss_net_profit DECIMAL(9,2))`,
			nSales, (*loader).sales},
	}

	for _, t := range tables {
		if *force {
			must(cli.Exec(ctx, "DROP TABLE IF EXISTS "+fq(t.name)))
		}
		must(cli.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+fq(t.name)+" "+t.ddl))
		if n := count(ctx, cli, fq(t.name)); n == t.rows {
			log.Printf("%-12s already loaded (%d rows), skipping", t.name, n)
			continue
		} else if n > 0 {
			log.Printf("%-12s has %d rows (want %d): reloading", t.name, n, t.rows)
			must(cli.Exec(ctx, "DROP TABLE IF EXISTS "+fq(t.name)))
			must(cli.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+fq(t.name)+" "+t.ddl))
		}
		// Each table gets its own deterministic stream regardless of which
		// tables were skipped.
		l := &loader{
			ctx: ctx, cli: cli, dialect: target.dialect, table: fq(t.name),
			rng: rand.New(rand.NewSource(seed + int64(len(t.name)))),
		}
		start := time.Now()
		t.load(l)
		l.flush()
		log.Printf("%-12s loaded %d rows in %s", t.name, count(ctx, cli, fq(t.name)), time.Since(start).Round(time.Second))
	}
	log.Printf("demo data ready in %s.%s (%s profile)", target.catalog, db, *profileName)
}

// loader batches multi-row INSERT statements.
type loader struct {
	ctx     context.Context
	cli     dbclient.Client
	dialect emitter.Dialect
	table   string
	rng     *rand.Rand
	buf     []string
}

const batchSize = 500

func (l *loader) add(row string) {
	l.buf = append(l.buf, "("+row+")")
	if len(l.buf) >= batchSize {
		l.flush()
	}
}

func (l *loader) flush() {
	if len(l.buf) == 0 {
		return
	}
	must(l.cli.Exec(l.ctx, "INSERT INTO "+l.table+" VALUES "+strings.Join(l.buf, ",")))
	l.buf = l.buf[:0]
}

func (l *loader) dates() {
	day := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < nDates; i++ {
		sk, _ := strconv.Atoi(day.Format("20060102"))
		q := (int(day.Month())-1)/3 + 1
		l.add(fmt.Sprintf("%d, %s, %d, %d, '%dQ%d', '%s', '%s'",
			sk, dateLiteral(l.dialect, day), day.Year(), int(day.Month()),
			day.Year(), q, day.Month().String(), day.Weekday().String()))
		day = day.AddDate(0, 0, 1)
	}
}

func (l *loader) stores() {
	for i := 1; i <= nStores; i++ {
		employees := 50 + l.rng.Intn(250)
		l.add(fmt.Sprintf("%d, 'S%04d', 'Store #%d', '%s', '%s', %d",
			i, i, i, cities[l.rng.Intn(len(cities))], states[(i-1)%len(states)], employees))
	}
}

func (l *loader) items() {
	for i := 1; i <= nItems; i++ {
		cat := categories[l.rng.Intn(len(categories))]
		brand := fmt.Sprintf("%s #%d", brandWords[l.rng.Intn(len(brandWords))], 1+l.rng.Intn(9))
		price := float64(1+l.rng.Intn(9900)) / 100 * 3
		l.add(fmt.Sprintf("%d, 'I%06d', 'Item %d in %s', '%s', '%s', %.2f",
			i, i, i, cat, brand, cat, price))
	}
}

func (l *loader) customers() {
	for i := 1; i <= nCustomers; i++ {
		fn := firstNames[l.rng.Intn(len(firstNames))]
		ln := lastNames[l.rng.Intn(len(lastNames))]
		l.add(fmt.Sprintf("%d, 'C%07d', '%s', '%s', '%s.%s.%d@example.com', %d",
			i, i, fn, ln, strings.ToLower(fn), strings.ToLower(ln), i, 1940+l.rng.Intn(60)))
	}
}

func (l *loader) sales() {
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= nSales; i++ {
		day := base.AddDate(0, 0, l.rng.Intn(nDates))
		dateSK, _ := strconv.Atoi(day.Format("20060102"))
		item := 1 + l.rng.Intn(nItems)
		// ~2% of sales have no customer record (NULL), a realistic wrinkle
		// that distinguishes COUNT(customer) semantics in the benchmark.
		cust := "NULL"
		if l.rng.Intn(100) >= 2 {
			cust = strconv.Itoa(1 + l.rng.Intn(nCustomers))
		}
		store := 1 + l.rng.Intn(nStores)
		qty := 1 + l.rng.Intn(20)
		price := float64(50+l.rng.Intn(29950)) / 100
		ext := float64(qty) * price
		profit := ext * (float64(l.rng.Intn(50))/100 - 0.10) // -10% .. +39%
		l.add(fmt.Sprintf("%d, %d, %s, %d, %d, %d, %.2f, %.2f, %.2f",
			dateSK, item, cust, store, i, qty, price, ext, profit))
	}
}

type sqlTarget struct {
	dialect           emitter.Dialect
	catalog, database string
}

func newSQLTarget(engine, catalog, database string) (sqlTarget, error) {
	dialect, err := emitter.Get(engine)
	if err != nil {
		return sqlTarget{}, err
	}
	if catalog == "" {
		return sqlTarget{}, fmt.Errorf("ENGINE_CATALOG must not be empty")
	}
	if database == "" {
		return sqlTarget{}, fmt.Errorf("DEMO_DATABASE must not be empty")
	}
	return sqlTarget{dialect: dialect, catalog: catalog, database: database}, nil
}

func (t sqlTarget) catalogProbe() string {
	if t.dialect.Name() == "trino" {
		return "SHOW SCHEMAS FROM " + t.dialect.QuoteIdent(t.catalog)
	}
	return "SHOW DATABASES FROM " + t.dialect.QuoteIdent(t.catalog)
}

func (t sqlTarget) createSchema() string {
	qualified := t.dialect.QuoteIdent(t.catalog) + "." + t.dialect.QuoteIdent(t.database)
	return t.dialect.CreateSchema(qualified)
}

func (t sqlTarget) table(name string) string {
	return t.dialect.QualifyTable(t.catalog, t.database, name)
}

func dateLiteral(dialect emitter.Dialect, day time.Time) string {
	literal, err := dialect.Literal(day.UTC().Format("2006-01-02"))
	if err != nil {
		panic(err) // every registered dialect supports string literals
	}
	if dialect.Name() == "trino" {
		return "DATE " + literal
	}
	return literal
}

func loaderCatalog() string {
	if catalog := os.Getenv("ENGINE_CATALOG"); catalog != "" {
		return catalog
	}
	return envOr("ICEBERG_CATALOG", "iceberg")
}

func count(ctx context.Context, cli dbclient.Client, fqTable string) int {
	_, rows, err := cli.Query(ctx, dbclient.EngineCredential{}, "SELECT COUNT(*) FROM "+fqTable)
	if err != nil || len(rows) == 0 {
		return -1
	}
	n, _ := strconv.Atoi(fmt.Sprint(rows[0][0]))
	return n
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
