# Superset over governed metric views

Superset connects to StarRocks over MySQL protocol and queries the governed
views the operator created in `semantic_views`. Analysts see certified
numbers; they cannot re-derive `store_productivity` wrongly because the view
already computed it through the planner.

## 1. Add the StarRocks database connection

In Superset: Settings > Database Connections > + Database > Other.

SQLAlchemy URI (adjust host/credentials to your cluster; these are the same
values you passed to the Helm chart):

```
starrocks://root:<password>@starrocks-fe.starrocks.svc.cluster.local:9030/semantic_views
```

If the `starrocks` dialect is not installed in your Superset image, the
MySQL dialect works for queries:

```
mysql://root:<password>@starrocks-fe.starrocks.svc.cluster.local:9030/semantic_views
```

To install the native dialect instead, add `starrocks` to your Superset
image's pip requirements and rebuild (see Superset docs on database drivers).

Test the connection, then save as `StarRocks Semantic Views`.

## 2. Register the governed views as datasets

Datasets > + Dataset > database `StarRocks Semantic Views`, schema
`semantic_views`. Register each view published by the demo SemanticModel:

| View | Content |
|---|---|
| `sales_by_category_year` | total_sales, total_profit, total_quantity by category and year |
| `sales_by_brand_year` | sales_by_brand by brand and year |
| `monthly_sales` | total_sales, total_profit, transaction_count by month |
| `store_productivity_by_state` | fan-out-safe sales per employee by state |
| `clv_by_year` | customer_lifetime_value by year |

The view list is in the SemanticModel CR (`spec.views`); `kubectl get
semanticmodel tpcds-retail -n semantic-system -o yaml` shows their status.

## 3. Build the comparison dashboard

Create a dashboard `Semantic Layer Demo` with:

1. Chart `Revenue by category` (bar): dataset `sales_by_category_year`,
   x `item.i_category`, metric `SUM(total_sales)`, series by `date_dim.d_year`.
2. Chart `Monthly revenue` (line): dataset `monthly_sales`,
   x `date_dim.d_date`, metrics `SUM(total_sales)`, `SUM(total_profit)`.
3. Chart `Sales per employee by state` (bar): dataset
   `store_productivity_by_state`, x `store.s_state`, metric
   `AVG(store_productivity)`. This is the certified fan-out-safe number.
4. Chart `CLV by year` (big number / bar): dataset `clv_by_year`.

Column names in the views are the semantic references (`item.i_category`,
`total_sales`), so charts read naturally.

## 4. Show the failure mode inside Superset

Add a SQL Lab tab with the naive query an analyst (or an LLM) writes for
"sales per employee by state":

```sql
SELECT s.s_state,
       SUM(ss.ss_ext_sales_price) / SUM(s.s_number_employees) AS naive_sales_per_employee
FROM iceberg.osi_demo.store_sales ss
JOIN iceberg.osi_demo.store s ON ss.ss_store_sk = s.s_store_sk
GROUP BY s.s_state;
```

Compare it with the governed view:

```sql
SELECT * FROM semantic_views.store_productivity_by_state;
```

The naive number is wrong by orders of magnitude: joining store to the fact
table repeats each store's headcount once per sales row, so the denominator
is inflated by the row count. The governed view splits the ratio into two
aggregations and deduplicates headcount on the store primary key. Put both
result grids side by side on the dashboard; that is the demo.
