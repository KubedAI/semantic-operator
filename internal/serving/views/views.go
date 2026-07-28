// Package views materializes governed metric views in the query engine. Each view
// body is planner output, so BI users querying the view get exactly the
// certified metric definition, governance included.
package views

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner"
)

// Executor is the DDL surface the publisher needs (satisfied by
// the engine client, mocked in tests).
type Executor interface {
	Exec(ctx context.Context, sql string) error
}

// DefaultDatabase is where governed views live unless the CR overrides it.
const DefaultDatabase = "semantic_views"

// BuildViewSQL compiles one ViewSpec into its CREATE OR REPLACE VIEW DDL.
func BuildViewSQL(cm *planner.CompiledModel, d emitter.Dialect, v v1alpha1.ViewSpec, database, defaultRole string) (string, error) {
	role := v.Role
	if role == "" {
		role = defaultRole
	}
	req := planner.Request{
		Metrics:    v.Metrics,
		Dimensions: v.Dimensions,
		TimeGrain:  v.TimeGrain,
	}
	for _, f := range v.Filters {
		pf := planner.Filter{Field: f.Field, Op: f.Op}
		switch {
		case len(f.Values) > 0:
			for _, s := range f.Values {
				pf.Values = append(pf.Values, coerce(s, f.ValueType))
			}
		default:
			pf.Value = coerce(f.Value, f.ValueType)
		}
		req.Filters = append(req.Filters, pf)
	}
	plan, err := planner.Build(cm, d, req, governance.Single(role))
	if err != nil {
		return "", fmt.Errorf("view %q: %w", v.Name, err)
	}
	return fmt.Sprintf("CREATE OR REPLACE VIEW %s.%s AS\n%s",
		quoteDotted(d, database), d.QuoteIdent(v.Name), plan.SQL), nil
}

// quoteDotted quotes a possibly catalog-qualified schema name part by part:
// "semantic_views" for engines with a default catalog (StarRocks), or
// "iceberg.semantic_views" for engines where views live inside a catalog
// (Trino). Each dot-separated part is quoted through the dialect.
func quoteDotted(d emitter.Dialect, name string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		parts[i] = d.QuoteIdent(p)
	}
	return strings.Join(parts, ".")
}

// coerce renders a string-typed CR filter value per valueType: auto tries
// integer then float then string.
func coerce(s, valueType string) any {
	switch valueType {
	case "string":
		return s
	case "number":
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return s
	default: // auto
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return s
	}
}

// Publish creates or replaces every declared view and returns their names.
func Publish(ctx context.Context, exec Executor, cm *planner.CompiledModel, d emitter.Dialect, specViews []v1alpha1.ViewSpec, database, defaultRole string) ([]string, error) {
	if len(specViews) == 0 {
		return nil, nil
	}
	if database == "" {
		database = DefaultDatabase
	}
	if err := exec.Exec(ctx, d.CreateSchema(quoteDotted(d, database))); err != nil {
		return nil, fmt.Errorf("creating view database %q: %w", database, err)
	}
	var created []string
	for _, v := range specViews {
		ddl, err := BuildViewSQL(cm, d, v, database, defaultRole)
		if err != nil {
			return created, err
		}
		if err := exec.Exec(ctx, ddl); err != nil {
			return created, fmt.Errorf("creating view %q: %w", v.Name, err)
		}
		created = append(created, v.Name)
	}
	return created, nil
}

// Drop removes views the operator created but that are no longer declared.
func Drop(ctx context.Context, exec Executor, d emitter.Dialect, database string, names []string) error {
	if database == "" {
		database = DefaultDatabase
	}
	var errs []string
	for _, n := range names {
		if err := exec.Exec(ctx, "DROP VIEW IF EXISTS "+quoteDotted(d, database)+"."+d.QuoteIdent(n)); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", n, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("dropping views: %s", strings.Join(errs, "; "))
	}
	return nil
}
