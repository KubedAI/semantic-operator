# Benchmark results

Generated 2026-07-19T19:44:06Z. Bedrock model `us.anthropic.claude-sonnet-4-5-20250929-v1:0`, temperature 0. 30 questions, 3 phrasings each.

Definitions: accuracy = phrasing runs matching ground truth (0.5% numeric tolerance); consistency = questions where all three phrasings return the same answer; hallucination = failed runs that referenced nonexistent tables or columns.

## Summary

| Path | Accuracy | Consistency across paraphrases | Hallucination rate | Wrong | Failed | No query |
|---|---|---|---|---|---|---|
| Raw text-to-SQL | 62/90 (69%) | 19/30 (63%) | 0/90 (0%) | 28 | 0 | 0 |
| Semantic layer (MCP) | 87/90 (97%) | 27/30 (90%) | 0/90 (0%) | 3 | 0 | 0 |

## Per-question verdicts

| Question | Raw (q, p1, p2) | Semantic (q, p1, p2) |
|---|---|---|
| q01 What was total sales revenue in 2001? | ok, ok, ok | ok, ok, ok |
| q02 What was total sales by product category in 2001? | ok, ok, ok | ok, ok, ok |
| q03 What was total net profit in 2002? | ok, ok, ok | ok, ok, ok |
| q04 Show monthly total sales for 2001 by calendar month. | WRONG, WRONG, WRONG | ok, ok, ok |
| q05 How many units were sold per category in 2000? | ok, ok, ok | ok, ok, ok |
| q06 How many sales transactions were there in 2001? | ok, ok, ok | ok, ok, ok |
| q07 What is total sales revenue by store state? | ok, ok, ok | ok, ok, ok |
| q08 What was revenue in quarter 2001Q3? | ok, WRONG, WRONG | ok, ok, ok |
| q09 What is the customer lifetime value? | WRONG, WRONG, WRONG | ok, ok, ok |
| q10 What was customer lifetime value for purchases made in 2001? | WRONG, WRONG, ok | ok, ok, ok |
| q11 What is our overall sales per employee? | WRONG, WRONG, WRONG | ok, ok, ok |
| q12 What is sales per employee by store state? | WRONG, WRONG, WRONG | ok, ok, ok |
| q13 What was net profit by category in 2001? | ok, ok, ok | ok, ok, ok |
| q14 What was revenue for the Books category by year? | ok, ok, ok | ok, ok, ok |
| q15 How many units were sold in December 2001? | ok, ok, ok | ok, ok, ok |
| q16 How many transactions happened per store state in 2000? | ok, ok, ok | ok, ok, ok |
| q17 What was revenue from Texas stores in 2001? | WRONG, ok, ok | ok, ok, ok |
| q18 What is customer lifetime value for California store purchas... | WRONG, WRONG, ok | ok, ok, ok |
| q19 Show revenue by quarter for 2002. | ok, ok, ok | ok, ok, ok |
| q20 What is sales per employee for Texas stores? | WRONG, WRONG, WRONG | ok, ok, WRONG |
| q21 How many units of brand 'scholar #1' did we sell in 2001? | ok, ok, ok | ok, WRONG, ok |
| q22 What was net profit by store state in 2002? | ok, ok, ok | ok, ok, ok |
| q23 Show total sales by month name for 2000. | ok, ok, ok | ok, ok, ok |
| q24 What is total revenue by product category across all years? | ok, ok, ok | ok, ok, ok |
| q25 Show yearly revenue for 2000 and 2001. | ok, ok, ok | ok, ok, ok |
| q26 What was net profit for Electronics in 2001Q2? | ok, ok, ok | ok, ok, ok |
| q27 What is customer lifetime value by year? | WRONG, WRONG, WRONG | ok, ok, ok |
| q28 What is sales per employee for California stores in 2002? | WRONG, WRONG, WRONG | ok, ok, ok |
| q29 Show revenue by store state and year for the Jewelry categor... | ok, ok, ok | ok, ok, ok |
| q30 How many transactions and how much revenue did we have in 20... | ok, ok, ok | ok, WRONG, ok |

## Notable raw-path failures

### q04

```sql
SELECT 
    d.d_moy,
    d.d_month_name,
    SUM(ss.ss_ext_sales_price) AS total_sales
FROM iceberg.osi_demo.store_sales ss
JOIN iceberg.osi_demo.date_dim d ON ss.ss_sold_date_sk = d.d_date_sk
WHERE d.d_year = 2001
GROUP BY d.d_moy, d.d_month_name
ORDER BY d.d_moy;
```

Executed without error, returned a wrong number.

### q08

```sql
SELECT SUM(ss_ext_sales_price) AS total_sales
FROM iceberg.osi_demo.store_sales
JOIN iceberg.osi_demo.date_dim ON store_sales.ss_sold_date_sk = date_dim.d_date_sk
WHERE date_dim.d_year = 2001 
  AND date_dim.d_quarter_name = 'Q3';
```

Executed without error, returned a wrong number.

### q09

```sql
SELECT 
    c.c_customer_sk,
    c.c_customer_id,
    c.c_first_name,
    c.c_last_name,
    SUM(ss.ss_ext_sales_price) AS lifetime_value,
    SUM(ss.ss_net_profit) AS lifetime_profit,
    COUNT(DISTINCT ss.ss_ticket_number) AS total_transactions,
    SUM(ss.ss_quantity) AS total_items_purchased
FROM iceberg.osi_demo.store_sales ss
INNER JOIN iceberg.osi_demo.customer c ON ss.ss_customer_sk = c.c_customer_sk
GROUP BY 
    c.c_customer_sk,
    c.c_customer_id,
    c.c_first_name,
    c.c_last_name
ORDER BY lifetime_value DESC
```

Executed without error, returned a wrong number.

### q10

```sql
SELECT 
    c.c_customer_sk,
    c.c_customer_id,
    c.c_first_name,
    c.c_last_name,
    SUM(ss.ss_ext_sales_price) AS lifetime_value
FROM iceberg.osi_demo.store_sales ss
INNER JOIN iceberg.osi_demo.date_dim d ON ss.ss_sold_date_sk = d.d_date_sk
INNER JOIN iceberg.osi_demo.customer c ON ss.ss_customer_sk = c.c_customer_sk
WHERE d.d_year = 2001
GROUP BY 
    c.c_customer_sk,
    c.c_customer_id,
    c.c_first_name,
    c.c_last_name
ORDER BY lifetime_value DESC
```

Executed without error, returned a wrong number.

### q11

```sql
SELECT 
    SUM(ss.ss_ext_sales_price) / SUM(s.s_number_employees) AS sales_per_employee
FROM iceberg.osi_demo.store_sales ss
JOIN iceberg.osi_demo.store s ON ss.ss_store_sk = s.s_store_sk
WHERE s.s_number_employees IS NOT NULL;
```

Executed without error, returned a wrong number.

