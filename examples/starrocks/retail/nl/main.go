// This tool answers one business question two ways and prints the SQL and
// results side by side: raw text-to-SQL (LLM sees table DDL) versus the
// semantic layer (LLM selects certified metrics via MCP).
//
// Env:
//
//	STARROCKS_HOST/PORT/USER/PASSWORD   raw-path execution and schema fetch
//	ICEBERG_CATALOG (iceberg)           external catalog with the demo tables
//	DEMO_DATABASE   (osi_demo)
//	MCP_ENDPOINT    (http://localhost:8090/mcp)
//	SEMANTIC_ROLE   (analyst)
//	AWS_REGION, BEDROCK_MODEL_ID        Bedrock Converse model (temperature 0)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/KubedAI/ossie-semantic-operator/internal/nlbench"
	"github.com/KubedAI/ossie-semantic-operator/internal/starrocks"
)

func main() {
	question := flag.String("question", "", "business question to answer")
	path := flag.String("path", "both", "raw | semantic | both")
	flag.Parse()
	if *question == "" {
		log.Fatal("-question is required")
	}

	ctx := context.Background()
	host := os.Getenv("STARROCKS_HOST")
	if host == "" {
		log.Fatal("STARROCKS_HOST is required")
	}
	db, err := starrocks.Open(starrocks.Config{
		Host: host, Port: envInt("STARROCKS_PORT", 9030),
		User: envOr("STARROCKS_USER", "root"), Password: os.Getenv("STARROCKS_PASSWORD"),
	})
	if err != nil {
		log.Fatal(err)
	}
	llm, err := nlbench.NewLLM(ctx, os.Getenv("AWS_REGION"), envOr("BEDROCK_MODEL_ID", "us.anthropic.claude-sonnet-4-5-20250929-v1:0"))
	if err != nil {
		log.Fatal(err)
	}

	r := &nlbench.Runner{
		LLM: llm, DB: db,
		Catalog:     envOr("ICEBERG_CATALOG", "iceberg"),
		DB_:         envOr("DEMO_DATABASE", "osi_demo"),
		MCPEndpoint: envOr("MCP_ENDPOINT", "http://localhost:8090/mcp"),
		Role:        envOr("SEMANTIC_ROLE", "analyst"),
		Tables:      []string{"store_sales", "date_dim", "customer", "item", "store"},
	}

	fmt.Printf("Question: %s\n", *question)
	if *path == "raw" || *path == "both" {
		printResult(r.AnswerRaw(ctx, *question), "WITHOUT semantic layer (raw text-to-SQL)")
	}
	if *path == "semantic" || *path == "both" {
		printResult(r.AnswerSemantic(ctx, *question), "WITH semantic layer (MCP + planner)")
	}
}

func printResult(res nlbench.PathResult, title string) {
	line := strings.Repeat("=", 72)
	fmt.Printf("\n%s\n%s\n%s\n", line, title, line)
	if len(res.ToolCalls) > 0 {
		fmt.Printf("Tool calls: %s\n", strings.Join(res.ToolCalls, " -> "))
	}
	if res.SQL != "" {
		fmt.Printf("\nSQL:\n%s\n", res.SQL)
	}
	if res.Err != "" {
		fmt.Printf("\nERROR: %s\n", res.Err)
	}
	if res.Columns != nil {
		fmt.Printf("\nResult (%d rows):\n", len(res.Rows))
		fmt.Println(strings.Join(res.Columns, " | "))
		for i, row := range res.Rows {
			if i >= 15 {
				fmt.Printf("... %d more rows\n", len(res.Rows)-15)
				break
			}
			cells := make([]string, len(row))
			for j, c := range row {
				cells[j] = fmt.Sprint(c)
			}
			fmt.Println(strings.Join(cells, " | "))
		}
	}
	if res.Answer != "" {
		fmt.Printf("\nModel answer:\n%s\n", strings.TrimSpace(res.Answer))
	}
	fmt.Printf("\n(%d ms)\n", res.ElapsedMs)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}
