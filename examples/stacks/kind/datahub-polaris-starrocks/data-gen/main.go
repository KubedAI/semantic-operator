// Command data-gen generates the fixed small (~44k-row) saas-accounts
// dataset and loads it into a StarRocks Iceberg external catalog over the
// MySQL protocol. It is standalone: no AWS, no profiles, no manifest, no reset.
//
//	go run . --host 127.0.0.1 --port 9030 --user root --catalog iceberg
//	go run . --count-only        # print generated row counts, no connection
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"

	"account-demo.local/datagen/constants"
	"account-demo.local/datagen/gen"
	"account-demo.local/datagen/loader"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatalf("data-gen: %v", err)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("data-gen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	host := fs.String("host", "127.0.0.1", "StarRocks FE host")
	port := fs.Int("port", 9030, "StarRocks FE MySQL port")
	user := fs.String("user", "root", "StarRocks username")
	password := fs.String("password", "", "StarRocks password (prefer via env/secret)")
	catalog := fs.String("catalog", "iceberg", "StarRocks Iceberg external catalog name")
	database := fs.String("database", constants.DatabaseName, "Iceberg namespace/database")
	batchSize := fs.Int("batch-size", loader.DefaultBatchSize, "rows per INSERT (1..5000)")
	countOnly := fs.Bool("count-only", false, "generate all tables and print row counts without connecting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	if *countOnly {
		return printCounts()
	}

	ctx := context.Background()
	db, err := openStarRocks(ctx, *host, *port, *user, *password)
	if err != nil {
		return err
	}
	defer db.Close()

	dataLoader, err := loader.New(loader.SQLDB{DB: db}, loader.Config{
		Catalog:   *catalog,
		Database:  *database,
		BatchSize: *batchSize,
	}, func(format string, a ...any) { log.Printf(format, a...) })
	if err != nil {
		return err
	}
	log.Printf("loading small dataset into `%s`.`%s` via %s", *catalog, *database, net.JoinHostPort(*host, strconv.Itoa(*port)))
	if err := dataLoader.Run(ctx); err != nil {
		return err
	}
	log.Printf("done")
	return nil
}

func openStarRocks(ctx context.Context, host string, port int, user, password string) (*sql.DB, error) {
	if host == "" {
		return nil, fmt.Errorf("--host is required")
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d", port)
	}
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	cfg.User = user
	cfg.Passwd = password
	cfg.InterpolateParams = true // interpolate time.Time/nil args client-side
	cfg.ParseTime = true
	cfg.Timeout = 15 * time.Second
	cfg.ReadTimeout = 10 * time.Minute
	cfg.WriteTimeout = 10 * time.Minute

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open StarRocks: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to StarRocks at %s: %w", cfg.Addr, err)
	}
	return db, nil
}

// printCounts streams every table and reports its generated row count. It runs
// the full deterministic generation (exercising all invariants) without a
// database connection, so it doubles as an offline self-check.
func printCounts() error {
	total := 0
	for _, table := range gen.Tables() {
		n := 0
		if err := gen.Rows(table, 4096, func(rows []gen.Row) error {
			n += len(rows)
			return nil
		}); err != nil {
			return err
		}
		expected := constants.ExpectedTableRowCounts[table]
		status := "ok"
		if n != expected {
			status = fmt.Sprintf("MISMATCH (want %d)", expected)
		}
		fmt.Printf("%-32s %8d  %s\n", table, n, status)
		total += n
	}
	fmt.Printf("%-32s %8d\n", "TOTAL", total)
	return nil
}
