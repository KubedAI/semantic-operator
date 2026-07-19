package planner

import (
	"errors"
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/starrocks"
	"github.com/KubedAI/semantic-operator/internal/governance"
)

func testDialect(t *testing.T) emitter.Dialect {
	t.Helper()
	d, err := emitter.Get("starrocks")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func fld(name, e string, isTime bool) v1alpha1.Field {
	f := v1alpha1.Field{Name: name, Expression: v1alpha1.Expression{Dialects: []v1alpha1.DialectExpression{{Dialect: "ANSI_SQL", Expression: e}}}}
	if isTime {
		f.Dimension = &v1alpha1.Dimension{IsTime: true}
	}
	return f
}

func metric(name, e string) v1alpha1.Metric {
	return v1alpha1.Metric{Name: name, Expression: v1alpha1.Expression{Dialects: []v1alpha1.DialectExpression{{Dialect: "ANSI_SQL", Expression: e}}}}
}

func testSpec() *v1alpha1.SemanticModelSpec {
	return &v1alpha1.SemanticModelSpec{
		Connection: v1alpha1.ConnectionSpec{Catalog: "iceberg", Database: "osi_demo"},
		Ossie: v1alpha1.OssieModel{
			Name: "tpcds_retail_model",
			Datasets: []v1alpha1.Dataset{
				{Name: "store_sales", Source: "store_sales", PrimaryKey: []string{"ss_item_sk", "ss_ticket_number"}, Fields: []v1alpha1.Field{
					fld("ss_sold_date_sk", "ss_sold_date_sk", false),
					fld("ss_customer_sk", "ss_customer_sk", false),
					fld("ss_item_sk", "ss_item_sk", false),
					fld("ss_store_sk", "ss_store_sk", false),
					fld("ss_ext_sales_price", "ss_ext_sales_price", false),
					fld("ss_net_profit", "ss_net_profit", false),
				}},
				{Name: "date_dim", Source: "date_dim", PrimaryKey: []string{"d_date_sk"}, Fields: []v1alpha1.Field{
					fld("d_date_sk", "d_date_sk", false),
					fld("d_date", "d_date", true),
					fld("d_year", "d_year", false),
				}},
				{Name: "customer", Source: "customer", PrimaryKey: []string{"c_customer_sk"}, Fields: []v1alpha1.Field{
					fld("c_customer_sk", "c_customer_sk", false),
					fld("c_email_address", "c_email_address", false),
					fld("customer_full_name", "c_first_name || ' ' || c_last_name", false),
				}},
				{Name: "item", Source: "item", PrimaryKey: []string{"i_item_sk"}, Fields: []v1alpha1.Field{
					fld("i_item_sk", "i_item_sk", false),
					fld("i_category", "i_category", false),
					fld("i_brand", "i_brand", false),
				}},
				{Name: "store", Source: "store", PrimaryKey: []string{"s_store_sk"}, Fields: []v1alpha1.Field{
					fld("s_store_sk", "s_store_sk", false),
					fld("s_state", "s_state", false),
					fld("s_number_employees", "s_number_employees", false),
				}},
			},
			Relationships: []v1alpha1.Relationship{
				{Name: "sales_to_date", From: "store_sales", To: "date_dim", FromColumns: []string{"ss_sold_date_sk"}, ToColumns: []string{"d_date_sk"}},
				{Name: "sales_to_customer", From: "store_sales", To: "customer", FromColumns: []string{"ss_customer_sk"}, ToColumns: []string{"c_customer_sk"}},
				{Name: "sales_to_item", From: "store_sales", To: "item", FromColumns: []string{"ss_item_sk"}, ToColumns: []string{"i_item_sk"}},
				{Name: "sales_to_store", From: "store_sales", To: "store", FromColumns: []string{"ss_store_sk"}, ToColumns: []string{"s_store_sk"}},
			},
			Metrics: []v1alpha1.Metric{
				metric("total_sales", "SUM(store_sales.ss_ext_sales_price)"),
				metric("total_profit", "SUM(store_sales.ss_net_profit)"),
				metric("customer_lifetime_value", "SUM(store_sales.ss_ext_sales_price) / COUNT(DISTINCT customer.c_customer_sk)"),
				metric("store_productivity", "SUM(store_sales.ss_ext_sales_price) / NULLIF(SUM(store.s_number_employees), 0)"),
			},
		},
		Governance: &v1alpha1.GovernanceSpec{
			DefaultRole: "analyst",
			Roles: []v1alpha1.RolePolicy{
				{Name: "analyst", AllowMetrics: []string{"total_*", "customer_lifetime_value", "store_productivity"},
					DenyFields: []string{"customer.c_email_address"}},
				{Name: "tx_analyst", AllowMetrics: []string{"*"},
					RowFilters: []v1alpha1.RowFilter{{Dataset: "store", Predicate: "s_state = 'TX'"}}},
				{Name: "admin", AllowMetrics: []string{"*"}},
			},
		},
	}
}

func compiled(t *testing.T) *CompiledModel {
	t.Helper()
	cm, err := Compile(testSpec(), "semantic-system", "tpcds-retail")
	if err != nil {
		t.Fatal(err)
	}
	return cm
}

func TestSimpleMetricByDimension(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
		Filters:    []Filter{{Field: "date_dim.d_year", Op: "=", Value: 2001}},
	}, governance.Identity{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	want := "/* semantic-layer model=tpcds_retail_model version=" + cm.Version + " request=" + plan.RequestHash + " */\n" +
		"SELECT `item`.`i_category` AS `item.i_category`,\n" +
		"       SUM(`store_sales`.`ss_ext_sales_price`) AS `total_sales`\n" +
		"FROM `iceberg`.`osi_demo`.`store_sales` AS `store_sales`\n" +
		"INNER JOIN `iceberg`.`osi_demo`.`date_dim` AS `date_dim` ON `store_sales`.`ss_sold_date_sk` = `date_dim`.`d_date_sk`\n" +
		"INNER JOIN `iceberg`.`osi_demo`.`item` AS `item` ON `store_sales`.`ss_item_sk` = `item`.`i_item_sk`\n" +
		"WHERE (`date_dim`.`d_year` = 2001)\n" +
		"GROUP BY 1\n" +
		"ORDER BY 1"
	if plan.SQL != want {
		t.Fatalf("SQL mismatch.\ngot:\n%s\n\nwant:\n%s", plan.SQL, want)
	}
}

func TestDeterminism(t *testing.T) {
	cm := compiled(t)
	req := Request{
		Metrics:    []string{"total_sales", "customer_lifetime_value", "store_productivity"},
		Dimensions: []string{"item.i_category", "date_dim.d_year"},
		Filters:    []Filter{{Field: "store.s_state", Op: "IN", Values: []any{"TX", "CA"}}},
	}
	first, err := Build(cm, testDialect(t), req, governance.Identity{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		p, err := Build(cm, testDialect(t), req, governance.Identity{Role: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		if p.SQL != first.SQL {
			t.Fatalf("iteration %d produced different SQL:\n%s\n\nvs\n\n%s", i, p.SQL, first.SQL)
		}
	}
}

func TestCLVIsInlineFanoutSafe(t *testing.T) {
	// COUNT DISTINCT denominator is fan-out safe: single-pass, no CTEs.
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"customer_lifetime_value"},
		Dimensions: []string{"date_dim.d_year"},
	}, governance.Identity{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.SQL, "WITH ") {
		t.Fatalf("CLV should compile single-pass, got:\n%s", plan.SQL)
	}
	if !strings.Contains(plan.SQL, "SUM(`store_sales`.`ss_ext_sales_price`) / NULLIF(COUNT(DISTINCT `customer`.`c_customer_sk`), 0)") {
		t.Fatalf("missing NULLIF-protected ratio:\n%s", plan.SQL)
	}
}

func TestStoreProductivitySplitsOnFanOut(t *testing.T) {
	// SUM over the one-side (store.s_number_employees) must not be summed
	// across the fact join: expect a dedup-on-primary-key denominator CTE.
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"store_productivity"},
		Dimensions: []string{"store.s_state"},
	}, governance.Identity{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	sql := plan.SQL
	if !strings.Contains(sql, "WITH ") {
		t.Fatalf("expected CTE split, got:\n%s", sql)
	}
	if !strings.Contains(sql, "`m_store_productivity_den`") {
		t.Fatalf("expected denominator CTE:\n%s", sql)
	}
	// Denominator aggregates over store only: dims and measure live on
	// store, so no fact join and no dedup wrapper are needed.
	if !strings.Contains(sql, "FROM `iceberg`.`osi_demo`.`store` AS `store`") {
		t.Fatalf("expected denominator rooted at store:\n%s", sql)
	}
	if !strings.Contains(sql, "/ NULLIF(`m_store_productivity_den`.`val`, 0)") {
		t.Fatalf("expected NULLIF on denominator value:\n%s", sql)
	}
}

func TestStoreProductivityDedupAcrossFactDims(t *testing.T) {
	// Dimension from another table forces the denominator through the fact:
	// employees must be deduplicated on store's primary key.
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"store_productivity"},
		Dimensions: []string{"item.i_category"},
	}, governance.Identity{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "SELECT DISTINCT `store`.`s_store_sk` AS `pk0`") {
		t.Fatalf("expected primary-key dedup subquery:\n%s", plan.SQL)
	}
}

func TestTimeGrain(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"date_dim.d_date"},
		TimeGrain:  "month",
	}, governance.Identity{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "DATE_TRUNC('month', `date_dim`.`d_date`)") {
		t.Fatalf("expected DATE_TRUNC:\n%s", plan.SQL)
	}
}

func TestTimeGrainRequiresTimeDimension(t *testing.T) {
	cm := compiled(t)
	_, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
		TimeGrain:  "month",
	}, governance.Identity{Role: "admin"})
	if err == nil || !strings.Contains(err.Error(), "time dimension") {
		t.Fatalf("expected time-dimension error, got %v", err)
	}
}

func TestComputedFieldDimension(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"customer.customer_full_name"},
	}, governance.Identity{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "(`customer`.`c_first_name` || ' ' || `customer`.`c_last_name`) AS `customer.customer_full_name`") {
		t.Fatalf("computed field not rendered:\n%s", plan.SQL)
	}
}

func TestGovernanceDeniedMetric(t *testing.T) {
	cm := compiled(t)
	// analyst's allowMetrics covers all four; restrict to test denial.
	cm.Governance.Roles[0].AllowMetrics = []string{"total_sales"}
	_, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_profit"}}, governance.Identity{Role: "analyst"})
	if !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestGovernanceDeniedFieldAsDimension(t *testing.T) {
	cm := compiled(t)
	_, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"customer.c_email_address"},
	}, governance.Identity{Role: "analyst"})
	if !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestGovernanceDeniedFieldInsideMetric(t *testing.T) {
	cm := compiled(t)
	cm.Governance.Roles[0].DenyFields = []string{"store_sales.ss_net_profit"}
	_, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_profit"}}, governance.Identity{Role: "analyst"})
	if !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for denied field inside metric, got %v", err)
	}
}

func TestGovernanceRowFilterInjected(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
	}, governance.Identity{Role: "tx_analyst"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "(`store`.`s_state` = 'TX')") {
		t.Fatalf("row filter missing:\n%s", plan.SQL)
	}
	// The row filter must pull the store join into the tree.
	if !strings.Contains(plan.SQL, "INNER JOIN `iceberg`.`osi_demo`.`store` AS `store`") {
		t.Fatalf("row filter did not pull in join:\n%s", plan.SQL)
	}
}

func TestGovernanceUnknownRoleDenied(t *testing.T) {
	cm := compiled(t)
	_, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_sales"}}, governance.Identity{Role: "ghost"})
	if !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestDefaultRoleApplied(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_sales"}}, governance.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Role != "analyst" {
		t.Fatalf("expected defaultRole analyst, got %q", plan.Role)
	}
}

func TestRequestHashStability(t *testing.T) {
	req := Request{Metrics: []string{"total_sales"}, Dimensions: []string{"item.i_category"}}
	a := RequestHash(req, "analyst")
	b := RequestHash(req, "analyst")
	if a != b {
		t.Fatal("hash not stable")
	}
	if RequestHash(req, "admin") == a {
		t.Fatal("hash must vary by role")
	}
	req2 := req
	req2.TimeGrain = "month"
	if RequestHash(req2, "analyst") == a {
		t.Fatal("hash must vary by grain")
	}
}

func TestRoleChangesPlanNotJustKey(t *testing.T) {
	cm := compiled(t)
	admin, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_sales"}}, governance.Identity{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_sales"}}, governance.Identity{Role: "tx_analyst"})
	if err != nil {
		t.Fatal(err)
	}
	if admin.SQL == tx.SQL {
		t.Fatal("row-filtered role must produce different SQL")
	}
	if admin.RequestHash == tx.RequestHash {
		t.Fatal("request hash must differ across roles")
	}
}

func TestUnknownMetricListsAvailable(t *testing.T) {
	cm := compiled(t)
	_, err := Build(cm, testDialect(t), Request{Metrics: []string{"revenue"}}, governance.Identity{Role: "admin"})
	if err == nil || !strings.Contains(err.Error(), "total_sales") {
		t.Fatalf("expected helpful unknown-metric error, got %v", err)
	}
}

func TestLimit(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_brand"},
		Limit:      10,
	}, governance.Identity{Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(plan.SQL, "LIMIT 10") {
		t.Fatalf("expected LIMIT 10:\n%s", plan.SQL)
	}
}

func TestSpecVersionChangesWithSpec(t *testing.T) {
	s1 := testSpec()
	s2 := testSpec()
	if SpecVersion(s1) != SpecVersion(s2) {
		t.Fatal("same spec must hash the same")
	}
	s2.Ossie.Metrics[0].Description = "changed"
	if SpecVersion(s1) == SpecVersion(s2) {
		t.Fatal("changed spec must change version")
	}
}
