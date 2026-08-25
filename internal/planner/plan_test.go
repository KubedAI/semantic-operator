package planner

import (
	"encoding/json"
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

func dimensionField(name, e string) v1alpha1.Field {
	f := fld(name, e, false)
	f.Dimension = &v1alpha1.Dimension{}
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
					dimensionField("d_year", "d_year"),
				}},
				{Name: "customer", Source: "customer", PrimaryKey: []string{"c_customer_sk"}, Fields: []v1alpha1.Field{
					fld("c_customer_sk", "c_customer_sk", false),
					dimensionField("c_email_address", "c_email_address"),
					dimensionField("customer_full_name", "c_first_name || ' ' || c_last_name"),
				}},
				{Name: "item", Source: "item", PrimaryKey: []string{"i_item_sk"}, Fields: []v1alpha1.Field{
					fld("i_item_sk", "i_item_sk", false),
					dimensionField("i_category", "i_category"),
					dimensionField("i_brand", "i_brand"),
				}},
				{Name: "store", Source: "store", PrimaryKey: []string{"s_store_sk"}, Fields: []v1alpha1.Field{
					fld("s_store_sk", "s_store_sk", false),
					dimensionField("s_state", "s_state"),
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

func TestCompilePreservesDimensionDeclarations(t *testing.T) {
	cm := compiled(t)
	if !cm.Datasets["item"].Fields["i_category"].IsDimension {
		t.Fatal("dimension declaration was not preserved")
	}
	date := cm.Datasets["date_dim"].Fields["d_date"]
	if !date.IsDimension || !date.IsTime {
		t.Fatalf("time dimension flags were not preserved: %+v", date)
	}
	if cm.Datasets["store_sales"].Fields["ss_ext_sales_price"].IsDimension {
		t.Fatal("undeclared measure was compiled as a dimension")
	}
}

func TestUndeclaredFieldRejectedAsDimensionButAllowedAsFilter(t *testing.T) {
	cm := compiled(t)
	_, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"store_sales.ss_ext_sales_price"},
	}, governance.Single("admin"))
	if err == nil || !strings.Contains(err.Error(), "not a declared dimension") {
		t.Fatalf("expected undeclared-dimension error, got %v", err)
	}

	_, err = Build(cm, testDialect(t), Request{
		Metrics: []string{"total_sales"},
		Filters: []Filter{{Field: "store_sales.ss_ext_sales_price", Op: ">", Value: 0}},
	}, governance.Single("admin"))
	if err != nil {
		t.Fatalf("undeclared field should remain filterable: %v", err)
	}
}

func TestSimpleMetricByDimension(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
		Filters:    []Filter{{Field: "date_dim.d_year", Op: "=", Value: 2001}},
	}, governance.Single("admin"))
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
		MetricFilters: []MetricFilter{
			{Metric: "store_productivity", Op: ">", Value: 10},
		},
		OrderBy: []OrderByClause{
			{Field: "store_productivity", Direction: "desc"},
			{Field: "item.i_category", Direction: "asc"},
		},
	}
	first, err := Build(cm, testDialect(t), req, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		p, err := Build(cm, testDialect(t), req, governance.Single("admin"))
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
	}, governance.Single("admin"))
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
	}, governance.Single("admin"))
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
	}, governance.Single("admin"))
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
	}, governance.Single("admin"))
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
	}, governance.Single("admin"))
	if err == nil || !strings.Contains(err.Error(), "time dimension") {
		t.Fatalf("expected time-dimension error, got %v", err)
	}
}

func TestComputedFieldDimension(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"customer.customer_full_name"},
	}, governance.Single("admin"))
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
	_, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_profit"}}, governance.Single("analyst"))
	if !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestGovernanceDeniedFieldAsDimension(t *testing.T) {
	cm := compiled(t)
	_, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"customer.c_email_address"},
	}, governance.Single("analyst"))
	if !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestGovernanceDeniedFieldInsideMetric(t *testing.T) {
	cm := compiled(t)
	cm.Governance.Roles[0].DenyFields = []string{"store_sales.ss_net_profit"}
	_, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_profit"}}, governance.Single("analyst"))
	if !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for denied field inside metric, got %v", err)
	}
}

func TestGovernanceRowFilterInjected(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
	}, governance.Single("tx_analyst"))
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
	_, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_sales"}}, governance.Single("ghost"))
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
	ordered := req
	ordered.OrderBy = []OrderByClause{{Field: "total_sales", Direction: "desc"}}
	orderedHash := RequestHash(ordered, "analyst")
	if orderedHash == a {
		t.Fatal("hash must vary by ordering")
	}
	ordered.OrderBy[0].Direction = "asc"
	if RequestHash(ordered, "analyst") == orderedHash {
		t.Fatal("hash must vary by ordering direction")
	}
}

func TestRoleChangesPlanNotJustKey(t *testing.T) {
	cm := compiled(t)
	admin, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_sales"}}, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_sales"}}, governance.Single("tx_analyst"))
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
	_, err := Build(cm, testDialect(t), Request{Metrics: []string{"revenue"}}, governance.Single("admin"))
	if err == nil || !strings.Contains(err.Error(), "total_sales") {
		t.Fatalf("expected helpful unknown-metric error, got %v", err)
	}
}

func TestOrderByMetricAndDimensionBeforeLimit(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
		OrderBy: []OrderByClause{
			{Field: "total_sales", Direction: "desc"},
			{Field: "item.i_category", Direction: "asc"},
		},
		Limit: 1,
	}, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(plan.SQL, "ORDER BY 2 DESC, 1 ASC\nLIMIT 1") {
		t.Fatalf("expected explicit ordering before LIMIT:\n%s", plan.SQL)
	}
}

func TestEmptyOrderByMatchesOmittedOrderBy(t *testing.T) {
	cm := compiled(t)
	base := Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
	}
	omitted, err := Build(cm, testDialect(t), base, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	base.OrderBy = []OrderByClause{}
	empty, err := Build(cm, testDialect(t), base, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	if empty.SQL != omitted.SQL || empty.RequestHash != omitted.RequestHash {
		t.Fatalf("empty orderBy must match omission:\nomitted: %s\nempty:   %s", omitted.SQL, empty.SQL)
	}
}

func TestOrderByDoesNotAppendDimensions(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category", "item.i_brand"},
		OrderBy:    []OrderByClause{{Field: "total_sales", Direction: "desc"}},
		Limit:      5,
	}, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(plan.SQL, "ORDER BY 3 DESC\nLIMIT 5") {
		t.Fatalf("planner added an ordering clause that was not requested:\n%s", plan.SQL)
	}
}

func TestOrderByCompositeMetric(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"store_productivity"},
		Dimensions: []string{"item.i_category"},
		OrderBy:    []OrderByClause{{Field: "store_productivity", Direction: "desc"}},
	}, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, "WITH ") {
		t.Fatalf("expected composite query:\n%s", plan.SQL)
	}
	if !strings.HasSuffix(plan.SQL, "ORDER BY 2 DESC") {
		t.Fatalf("expected final composite output to be ordered by metric:\n%s", plan.SQL)
	}
}

func TestOrderByValidation(t *testing.T) {
	base := Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
	}
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{
			name: "unrequested certified field",
			req: func() Request {
				r := base
				r.OrderBy = []OrderByClause{{Field: "total_profit", Direction: "desc"}}
				return r
			}(),
			want: "must reference a requested metric or dimension",
		},
		{
			name: "raw SQL expression",
			req: func() Request {
				r := base
				r.OrderBy = []OrderByClause{{Field: "SUM(total_sales)", Direction: "desc"}}
				return r
			}(),
			want: "must reference a requested metric or dimension",
		},
		{
			name: "uppercase direction",
			req: func() Request {
				r := base
				r.OrderBy = []OrderByClause{{Field: "total_sales", Direction: "DESC"}}
				return r
			}(),
			want: "use asc or desc",
		},
		{
			name: "missing direction",
			req: func() Request {
				r := base
				r.OrderBy = []OrderByClause{{Field: "total_sales"}}
				return r
			}(),
			want: "use asc or desc",
		},
		{
			name: "duplicate clause",
			req: func() Request {
				r := base
				r.OrderBy = []OrderByClause{
					{Field: "total_sales", Direction: "desc"},
					{Field: "total_sales", Direction: "asc"},
				}
				return r
			}(),
			want: "appears more than once",
		},
		{
			name: "ambiguous requested field",
			req: Request{
				Metrics: []string{"total_sales", "total_sales"},
				OrderBy: []OrderByClause{{Field: "total_sales", Direction: "desc"}},
			},
			want: "ambiguous",
		},
	}

	cm := compiled(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(cm, testDialect(t), tc.req, governance.Single("admin"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLimit(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_brand"},
		Limit:      10,
	}, governance.Single("admin"))
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

func TestMetricFilterOperators(t *testing.T) {
	cm := compiled(t)
	cases := []struct {
		name   string
		filter MetricFilter
		want   string
	}{
		{"equal", MetricFilter{Metric: "total_sales", Op: "=", Value: 100}, "HAVING (SUM(`store_sales`.`ss_ext_sales_price`) = 100)"},
		{"not equal", MetricFilter{Metric: "total_sales", Op: "!=", Value: 100}, "HAVING (SUM(`store_sales`.`ss_ext_sales_price`) != 100)"},
		{"less", MetricFilter{Metric: "total_sales", Op: "<", Value: 100}, "HAVING (SUM(`store_sales`.`ss_ext_sales_price`) < 100)"},
		{"less or equal", MetricFilter{Metric: "total_sales", Op: "<=", Value: 100}, "HAVING (SUM(`store_sales`.`ss_ext_sales_price`) <= 100)"},
		{"greater", MetricFilter{Metric: "total_sales", Op: ">", Value: 100}, "HAVING (SUM(`store_sales`.`ss_ext_sales_price`) > 100)"},
		{"greater or equal", MetricFilter{Metric: "total_sales", Op: ">=", Value: 100}, "HAVING (SUM(`store_sales`.`ss_ext_sales_price`) >= 100)"},
		{"between", MetricFilter{Metric: "total_sales", Op: "BETWEEN", Values: []any{100, 200}}, "HAVING (SUM(`store_sales`.`ss_ext_sales_price`) BETWEEN 100 AND 200)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Build(cm, testDialect(t), Request{
				Metrics:       []string{"total_sales"},
				MetricFilters: []MetricFilter{tc.filter},
			}, governance.Single("admin"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan.SQL, tc.want) {
				t.Fatalf("missing %q in:\n%s", tc.want, plan.SQL)
			}
		})
	}
}

func TestMetricFiltersUseHavingBeforeOrderAndLimit(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales", "total_profit"},
		Dimensions: []string{"item.i_category"},
		MetricFilters: []MetricFilter{
			{Metric: "total_sales", Op: ">", Value: 1000},
			{Metric: "total_profit", Op: "BETWEEN", Values: []any{10, 50}},
		},
		OrderBy: []OrderByClause{{Field: "total_sales", Direction: "desc"}},
		Limit:   5,
	}, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	want := "HAVING (SUM(`store_sales`.`ss_ext_sales_price`) > 1000)\n" +
		"   AND (SUM(`store_sales`.`ss_net_profit`) BETWEEN 10 AND 50)\n" +
		"ORDER BY 2 DESC\nLIMIT 5"
	if !strings.Contains(plan.SQL, want) {
		t.Fatalf("metric filters must be ANDed before ordering and limiting:\n%s", plan.SQL)
	}
}

func TestInlineRatioMetricFilterUsesExpandedExpression(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:       []string{"customer_lifetime_value"},
		Dimensions:    []string{"date_dim.d_year"},
		MetricFilters: []MetricFilter{{Metric: "customer_lifetime_value", Op: ">", Value: 500}},
	}, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	want := "HAVING (SUM(`store_sales`.`ss_ext_sales_price`) / NULLIF(COUNT(DISTINCT `customer`.`c_customer_sk`), 0) > 500)"
	if !strings.Contains(plan.SQL, want) {
		t.Fatalf("inline ratio filter did not use the expanded expression:\n%s", plan.SQL)
	}
	if strings.Contains(plan.SQL, "HAVING `customer_lifetime_value`") {
		t.Fatalf("HAVING must not use a SELECT alias:\n%s", plan.SQL)
	}
}

func TestCompositeMetricFiltersUseFinalValues(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, testDialect(t), Request{
		Metrics:    []string{"total_sales", "store_productivity"},
		Dimensions: []string{"item.i_category"},
		MetricFilters: []MetricFilter{
			{Metric: "store_productivity", Op: ">", Value: 2},
			{Metric: "total_sales", Op: "BETWEEN", Values: []any{100, 1000}},
		},
		OrderBy: []OrderByClause{{Field: "store_productivity", Direction: "desc"}},
		Limit:   10,
	}, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.SQL, "HAVING") {
		t.Fatalf("composite filters must not be pushed into aggregate CTEs:\n%s", plan.SQL)
	}
	want := "WHERE ((`m_store_productivity_num`.`val` / NULLIF(`m_store_productivity_den`.`val`, 0)) > 2)\n" +
		"  AND (`base`.`m_total_sales` BETWEEN 100 AND 1000)\n" +
		"ORDER BY 3 DESC\nLIMIT 10"
	if !strings.Contains(plan.SQL, want) {
		t.Fatalf("composite filters must use final metric values before order and limit:\n%s", plan.SQL)
	}
}

func TestMetricFilterValidation(t *testing.T) {
	base := Request{Metrics: []string{"total_sales"}}
	cases := []struct {
		name   string
		filter MetricFilter
		want   string
	}{
		{"unrequested metric", MetricFilter{Metric: "total_profit", Op: ">", Value: 1}, "requested certified metric"},
		{"raw expression", MetricFilter{Metric: "SUM(total_sales)", Op: ">", Value: 1}, "requested certified metric"},
		{"unsupported operator", MetricFilter{Metric: "total_sales", Op: "LIKE", Value: 1}, "unsupported operator"},
		{"case sensitive operator", MetricFilter{Metric: "total_sales", Op: "between", Values: []any{1, 2}}, "unsupported operator"},
		{"missing scalar value", MetricFilter{Metric: "total_sales", Op: ">"}, "non-null value"},
		{"null scalar value", MetricFilter{Metric: "total_sales", Op: ">", Value: nil}, "non-null value"},
		{"scalar with values", MetricFilter{Metric: "total_sales", Op: ">", Value: 1, Values: []any{2}}, "does not accept values"},
		{"between with value", MetricFilter{Metric: "total_sales", Op: "BETWEEN", Value: 1, Values: []any{2, 3}}, "does not accept value"},
		{"between wrong count", MetricFilter{Metric: "total_sales", Op: "BETWEEN", Values: []any{1}}, "exactly two values"},
		{"between null", MetricFilter{Metric: "total_sales", Op: "BETWEEN", Values: []any{nil, 2}}, "must not be null"},
		{"object value", MetricFilter{Metric: "total_sales", Op: ">", Value: map[string]any{"sql": "1"}}, "unsupported value type"},
		{"array value", MetricFilter{Metric: "total_sales", Op: ">", Value: []any{1}}, "unsupported value type"},
	}
	cm := compiled(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			req.MetricFilters = []MetricFilter{tc.filter}
			_, err := Build(cm, testDialect(t), req, governance.Single("admin"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestMetricFilterJSONIsStrict(t *testing.T) {
	cm := compiled(t)
	var req Request
	if err := json.Unmarshal([]byte(`{"metrics":["total_sales"],"metricFilters":[{"metric":"total_sales","op":"BETWEEN","value":null,"values":[1,2]}]}`), &req); err != nil {
		t.Fatal(err)
	}
	valid := Request{
		Metrics:       []string{"total_sales"},
		MetricFilters: []MetricFilter{{Metric: "total_sales", Op: "BETWEEN", Values: []any{float64(1), float64(2)}}},
	}
	if RequestHash(req, "admin") == RequestHash(valid, "admin") {
		t.Fatal("presence-sensitive invalid request must not share a cache key with a valid request")
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"value":null`) {
		t.Fatalf("explicit null field was lost during serialization: %s", encoded)
	}
	_, err = Build(cm, testDialect(t), req, governance.Single("admin"))
	if err == nil || !strings.Contains(err.Error(), "does not accept value") {
		t.Fatalf("explicit null value must not be treated as omitted, got %v", err)
	}

	err = json.Unmarshal([]byte(`{"metrics":["total_sales"],"metricFilters":[{"metric":"total_sales","op":">","value":1,"expression":"SUM(x)"}]}`), &req)
	if err == nil || !strings.Contains(err.Error(), "expression") {
		t.Fatalf("unknown nested property must be rejected, got %v", err)
	}
}

func TestMetricFilterUnauthorizedMetricFailsGovernance(t *testing.T) {
	cm := compiled(t)
	cm.Governance.Roles[0].AllowMetrics = []string{"total_sales"}
	_, err := Build(cm, testDialect(t), Request{
		Metrics:       []string{"total_profit"},
		MetricFilters: []MetricFilter{{Metric: "total_profit", Op: ">", Value: 0}},
	}, governance.Single("analyst"))
	if !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestMetricFiltersChangeRequestHash(t *testing.T) {
	base := Request{Metrics: []string{"total_sales"}}
	baseHash := RequestHash(base, "analyst")
	filtered := base
	filtered.MetricFilters = []MetricFilter{{Metric: "total_sales", Op: ">", Value: 100}}
	filteredHash := RequestHash(filtered, "analyst")
	if filteredHash == baseHash {
		t.Fatal("metric filters must change the request hash")
	}
	filtered.MetricFilters[0].Value = 101
	if RequestHash(filtered, "analyst") == filteredHash {
		t.Fatal("metric filter values must change the request hash")
	}
	filtered.MetricFilters[0].Op = ">="
	if RequestHash(filtered, "analyst") == filteredHash {
		t.Fatal("metric filter operators must change the request hash")
	}
}
