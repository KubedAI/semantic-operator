package main

import (
	"testing"
	"time"
)

func TestSQLTarget(t *testing.T) {
	tests := []struct {
		name       string
		engine     string
		catalog    string
		database   string
		wantProbe  string
		wantSchema string
		wantTable  string
		wantDate   string
	}{
		{
			name:       "starrocks",
			engine:     "starrocks",
			catalog:    "iceberg",
			database:   "osi_demo",
			wantProbe:  "SHOW DATABASES FROM `iceberg`",
			wantSchema: "CREATE DATABASE IF NOT EXISTS `iceberg`.`osi_demo`",
			wantTable:  "`iceberg`.`osi_demo`.`store_sales`",
			wantDate:   "'2001-02-03'",
		},
		{
			name:       "trino",
			engine:     "trino",
			catalog:    "memory",
			database:   "osi_demo",
			wantProbe:  "SHOW SCHEMAS FROM \"memory\"",
			wantSchema: "CREATE SCHEMA IF NOT EXISTS \"memory\".\"osi_demo\"",
			wantTable:  "\"memory\".\"osi_demo\".\"store_sales\"",
			wantDate:   "DATE '2001-02-03'",
		},
	}

	day := time.Date(2001, 2, 3, 17, 30, 0, 0, time.FixedZone("test", 2*60*60))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := newSQLTarget(tt.engine, tt.catalog, tt.database)
			if err != nil {
				t.Fatal(err)
			}
			if got := target.catalogProbe(); got != tt.wantProbe {
				t.Fatalf("catalogProbe() = %q, want %q", got, tt.wantProbe)
			}
			if got := target.createSchema(); got != tt.wantSchema {
				t.Fatalf("createSchema() = %q, want %q", got, tt.wantSchema)
			}
			if got := target.table("store_sales"); got != tt.wantTable {
				t.Fatalf("table() = %q, want %q", got, tt.wantTable)
			}
			if got := dateLiteral(target.dialect, day); got != tt.wantDate {
				t.Fatalf("dateLiteral() = %q, want %q", got, tt.wantDate)
			}
		})
	}
}

func TestNewSQLTargetRejectsInvalidConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name, engine, catalog, database string
	}{
		{name: "engine", engine: "unknown", catalog: "memory", database: "osi_demo"},
		{name: "catalog", engine: "trino", database: "osi_demo"},
		{name: "database", engine: "trino", catalog: "memory"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newSQLTarget(tt.engine, tt.catalog, tt.database); err == nil {
				t.Fatal("newSQLTarget() succeeded, want error")
			}
		})
	}
}

func TestLoaderCatalog(t *testing.T) {
	t.Setenv("ENGINE_CATALOG", "")
	t.Setenv("ICEBERG_CATALOG", "")
	if got := loaderCatalog(); got != "iceberg" {
		t.Fatalf("default loaderCatalog() = %q, want iceberg", got)
	}

	t.Setenv("ICEBERG_CATALOG", "legacy")
	if got := loaderCatalog(); got != "legacy" {
		t.Fatalf("legacy loaderCatalog() = %q, want legacy", got)
	}

	t.Setenv("ENGINE_CATALOG", "memory")
	if got := loaderCatalog(); got != "memory" {
		t.Fatalf("ENGINE_CATALOG loaderCatalog() = %q, want memory", got)
	}
}

func TestSelectDataProfile(t *testing.T) {
	full, err := selectDataProfile("full")
	if err != nil {
		t.Fatal(err)
	}
	if full != (dataProfile{dates: 1096, stores: 12, items: 500, customers: 2000, sales: 200_000}) {
		t.Fatalf("full profile = %+v", full)
	}

	e2e, err := selectDataProfile("e2e")
	if err != nil {
		t.Fatal(err)
	}
	if e2e != (dataProfile{dates: 1096, stores: 6, items: 25, customers: 100, sales: 2_000}) {
		t.Fatalf("e2e profile = %+v", e2e)
	}

	if _, err := selectDataProfile("tiny"); err == nil {
		t.Fatal("unknown profile succeeded, want error")
	}
}
