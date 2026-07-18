// Package nlbench answers natural-language business questions two ways for
// the demo and the benchmark:
//
//	raw:      the LLM sees SHOW CREATE TABLE output and writes StarRocks SQL
//	semantic: the LLM selects certified metrics/dimensions via MCP tools and
//	          the planner emits deterministic SQL
//
// Both paths execute against the same StarRocks cluster so the comparison
// is about query construction, not engines.
package nlbench

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/KubedAI/ossie-semantic-operator/internal/starrocks"
)

// PathResult is the outcome of one path answering one question.
type PathResult struct {
	Path      string   `json:"path"` // raw | semantic
	SQL       string   `json:"sql,omitempty"`
	Columns   []string `json:"columns,omitempty"`
	Rows      [][]any  `json:"rows,omitempty"`
	Answer    string   `json:"answer,omitempty"` // the LLM's final prose answer
	ToolCalls []string `json:"toolCalls,omitempty"`
	Err       string   `json:"error,omitempty"`
	ElapsedMs int64    `json:"elapsedMs"`
}

// Runner holds the clients both paths share.
type Runner struct {
	LLM     *LLM
	DB      *starrocks.Client
	Catalog string // StarRocks external catalog with the raw tables
	DB_     string // database within the catalog
	// MCPEndpoint is the semantic server's /mcp URL.
	MCPEndpoint string
	// Role is the identity passed to the semantic layer.
	Role string
	// Tables is the raw-path scope (the same tables the model binds).
	Tables []string
}

// RawSystemPrompt is intentionally a competent, good-faith text-to-SQL setup:
// the comparison is unfair only if the raw path is set up to fail.
const RawSystemPrompt = `You are an expert analytics engineer writing SQL for StarRocks
(MySQL-compatible syntax; DATE_TRUNC and standard aggregate functions are available).
You will be given CREATE TABLE statements and a business question.
Write exactly one SELECT statement that answers the question.
Qualify tables as they are named in the DDL. Use joins as needed.
Respond with the SQL only, inside a single ` + "```sql" + ` code block, no commentary.`

// AnswerRaw runs the without-semantic-layer path.
func (r *Runner) AnswerRaw(ctx context.Context, question string) PathResult {
	start := time.Now()
	res := PathResult{Path: "raw"}

	var ddls []string
	for _, t := range r.Tables {
		ddl, err := r.DB.ShowCreateTable(ctx, r.Catalog, r.DB_, t)
		if err != nil {
			res.Err = fmt.Sprintf("fetching schema for %s: %v", t, err)
			res.ElapsedMs = time.Since(start).Milliseconds()
			return res
		}
		// Present fully qualified names so the model can address the tables.
		ddls = append(ddls, fmt.Sprintf("-- table %s.%s.%s\n%s", r.Catalog, r.DB_, t, ddl))
	}

	user := fmt.Sprintf("Schemas:\n\n%s\n\nQuestion: %s", strings.Join(ddls, "\n\n"), question)
	reply, err := r.LLM.Complete(ctx, RawSystemPrompt, user)
	if err != nil {
		res.Err = fmt.Sprintf("llm: %v", err)
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res
	}
	sql := ExtractSQL(reply)
	if sql == "" {
		res.Err = "llm returned no SQL"
		res.Answer = reply
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res
	}
	res.SQL = sql
	cols, rows, err := r.DB.Query(ctx, sql)
	if err != nil {
		res.Err = fmt.Sprintf("execution: %v", err)
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res
	}
	res.Columns, res.Rows = cols, rows
	res.ElapsedMs = time.Since(start).Milliseconds()
	return res
}

// ExtractSQL pulls the statement out of a ```sql fence, or returns the whole
// trimmed reply when it already looks like a SELECT.
func ExtractSQL(reply string) string {
	if i := strings.Index(reply, "```sql"); i >= 0 {
		rest := reply[i+6:]
		if j := strings.Index(rest, "```"); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
	}
	if i := strings.Index(reply, "```"); i >= 0 {
		rest := reply[i+3:]
		if j := strings.Index(rest, "```"); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
	}
	trimmed := strings.TrimSpace(reply)
	if up := strings.ToUpper(trimmed); strings.HasPrefix(up, "SELECT") || strings.HasPrefix(up, "WITH") {
		return trimmed
	}
	return ""
}
