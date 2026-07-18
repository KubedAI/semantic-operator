// This runner executes the accuracy benchmark: every question and its
// paraphrases through both paths, compared against hand-written ground
// truth executed on the same StarRocks cluster. Output is a markdown
// report. Reproducibility: fixed demo data seed, temperature 0, and the
// model id is recorded in the report.
//
// Env: same as the nl comparison (examples/starrocks/retail/nl).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/KubedAI/ossie-semantic-operator/internal/nlbench"
	"github.com/KubedAI/ossie-semantic-operator/internal/starrocks"
)

type question struct {
	ID             string   `json:"id"`
	Question       string   `json:"question"`
	Paraphrases    []string `json:"paraphrases"`
	GroundTruthSQL string   `json:"ground_truth_sql"`
}

type questionFile struct {
	Questions []question `json:"questions"`
}

type phrasingOutcome struct {
	Verdict nlbench.Verdict
	Result  nlbench.PathResult
}

type pathStats struct {
	correct, wrong, failed, noquery, halluc int
	consistent, inconsistent                int
}

func main() {
	qfile := flag.String("questions", "examples/starrocks/retail/bench/questions.yaml", "question set")
	out := flag.String("out", "examples/starrocks/retail/bench/RESULTS.md", "markdown report path")
	limit := flag.Int("limit", 0, "run only the first N questions (0 = all)")
	paths := flag.String("paths", "both", "raw | semantic | both")
	flag.Parse()

	ctx := context.Background()
	host := os.Getenv("STARROCKS_HOST")
	if host == "" {
		log.Fatal("STARROCKS_HOST is required")
	}
	db, err := starrocks.Open(starrocks.Config{
		Host: host, Port: envInt("STARROCKS_PORT", 9030),
		User: envOr("STARROCKS_USER", "root"), Password: os.Getenv("STARROCKS_PASSWORD"),
		QueryTimeout: 2 * time.Minute,
	})
	if err != nil {
		log.Fatal(err)
	}
	modelID := envOr("BEDROCK_MODEL_ID", "us.anthropic.claude-sonnet-4-5-20250929-v1:0")
	llm, err := nlbench.NewLLM(ctx, os.Getenv("AWS_REGION"), modelID)
	if err != nil {
		log.Fatal(err)
	}
	catalog := envOr("ICEBERG_CATALOG", "iceberg")
	database := envOr("DEMO_DATABASE", "osi_demo")
	runner := &nlbench.Runner{
		LLM: llm, DB: db, Catalog: catalog, DB_: database,
		MCPEndpoint: envOr("MCP_ENDPOINT", "http://localhost:8090/mcp"),
		Role:        envOr("SEMANTIC_ROLE", "analyst"),
		Tables:      []string{"store_sales", "date_dim", "customer", "item", "store"},
	}

	raw, err := os.ReadFile(*qfile)
	if err != nil {
		log.Fatal(err)
	}
	var qf questionFile
	if err := yaml.Unmarshal(raw, &qf); err != nil {
		log.Fatal(err)
	}
	qs := qf.Questions
	if *limit > 0 && *limit < len(qs) {
		qs = qs[:*limit]
	}
	prefix := fmt.Sprintf("`%s`.`%s`.", catalog, database)

	stats := map[string]*pathStats{"raw": {}, "semantic": {}}
	perQuestion := map[string]map[string][]phrasingOutcome{} // qid -> path -> outcomes

	for _, q := range qs {
		gtSQL := strings.ReplaceAll(q.GroundTruthSQL, "$DB.", prefix)
		gtCols, gtRows, err := db.Query(ctx, gtSQL)
		if err != nil {
			log.Fatalf("%s: ground truth SQL failed (fix the question set): %v", q.ID, err)
		}
		phrasings := append([]string{q.Question}, q.Paraphrases...)
		perQuestion[q.ID] = map[string][]phrasingOutcome{}

		for _, path := range []string{"raw", "semantic"} {
			if *paths != "both" && *paths != path {
				continue
			}
			var outcomes []phrasingOutcome
			for _, phr := range phrasings {
				var res nlbench.PathResult
				if path == "raw" {
					res = runner.AnswerRaw(ctx, phr)
				} else {
					res = runner.AnswerSemantic(ctx, phr)
				}
				v := nlbench.Compare(gtCols, gtRows, res)
				outcomes = append(outcomes, phrasingOutcome{v, res})
				st := stats[path]
				switch v {
				case nlbench.VerdictCorrect:
					st.correct++
				case nlbench.VerdictWrong:
					st.wrong++
				case nlbench.VerdictFailed:
					st.failed++
					if nlbench.IsHallucination(res.Err) {
						st.halluc++
					}
				case nlbench.VerdictNoQuery:
					st.noquery++
				}
				log.Printf("%-4s %-8s %-8s %s", q.ID, path, v, truncate(phr, 60))
			}
			if allSameAnswer(outcomes) {
				stats[path].consistent++
			} else {
				stats[path].inconsistent++
			}
			perQuestion[q.ID][path] = outcomes
		}
	}

	report := buildReport(modelID, qs, perQuestion, stats, *paths)
	if err := os.WriteFile(*out, []byte(report), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("report written to %s", *out)
}

// allSameAnswer reports whether every phrasing produced the same verdict
// and, for correct/wrong ones, result sets that match each other. This is
// the paraphrase-consistency measure: a path can be consistently wrong,
// which is still better diagnosable than randomly wrong.
func allSameAnswer(outcomes []phrasingOutcome) bool {
	for i := 1; i < len(outcomes); i++ {
		if outcomes[i].Verdict != outcomes[0].Verdict {
			return false
		}
		if outcomes[0].Verdict == nlbench.VerdictCorrect || outcomes[0].Verdict == nlbench.VerdictWrong {
			// Compare result i against result 0 as if 0 were ground truth.
			if nlbench.Compare(outcomes[0].Result.Columns, outcomes[0].Result.Rows, outcomes[i].Result) != nlbench.VerdictCorrect {
				return false
			}
		}
	}
	return true
}

func buildReport(modelID string, qs []question, per map[string]map[string][]phrasingOutcome, stats map[string]*pathStats, paths string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Benchmark results\n\n")
	fmt.Fprintf(&b, "Generated %s. Bedrock model `%s`, temperature 0. %d questions, 3 phrasings each.\n\n",
		time.Now().UTC().Format(time.RFC3339), modelID, len(qs))
	fmt.Fprintf(&b, "Definitions: accuracy = phrasing runs matching ground truth (0.5%% numeric tolerance); ")
	fmt.Fprintf(&b, "consistency = questions where all three phrasings return the same answer; ")
	fmt.Fprintf(&b, "hallucination = failed runs that referenced nonexistent tables or columns.\n\n")

	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "| Path | Accuracy | Consistency across paraphrases | Hallucination rate | Wrong | Failed | No query |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|\n")
	for _, path := range []string{"raw", "semantic"} {
		if paths != "both" && paths != path {
			continue
		}
		st := stats[path]
		total := st.correct + st.wrong + st.failed + st.noquery
		if total == 0 {
			continue
		}
		qtotal := st.consistent + st.inconsistent
		fmt.Fprintf(&b, "| %s | %d/%d (%.0f%%) | %d/%d (%.0f%%) | %d/%d (%.0f%%) | %d | %d | %d |\n",
			pathLabel(path),
			st.correct, total, pct(st.correct, total),
			st.consistent, qtotal, pct(st.consistent, qtotal),
			st.halluc, total, pct(st.halluc, total),
			st.wrong, st.failed, st.noquery)
	}

	fmt.Fprintf(&b, "\n## Per-question verdicts\n\n")
	fmt.Fprintf(&b, "| Question | Raw (q, p1, p2) | Semantic (q, p1, p2) |\n|---|---|---|\n")
	for _, q := range qs {
		fmt.Fprintf(&b, "| %s %s | %s | %s |\n", q.ID, truncate(q.Question, 60),
			verdictCell(per[q.ID]["raw"]), verdictCell(per[q.ID]["semantic"]))
	}

	// Show the most instructive failures: wrong-but-confident raw SQL.
	fmt.Fprintf(&b, "\n## Notable raw-path failures\n\n")
	shown := 0
	var ids []string
	for id := range per {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, o := range per[id]["raw"] {
			if o.Verdict == nlbench.VerdictWrong && shown < 5 {
				fmt.Fprintf(&b, "### %s\n\n```sql\n%s\n```\n\nExecuted without error, returned a wrong number.\n\n", id, o.Result.SQL)
				shown++
				break
			}
		}
	}
	if shown == 0 {
		fmt.Fprintf(&b, "None recorded in this run.\n")
	}
	return b.String()
}

func verdictCell(outcomes []phrasingOutcome) string {
	if len(outcomes) == 0 {
		return "-"
	}
	marks := make([]string, len(outcomes))
	for i, o := range outcomes {
		switch o.Verdict {
		case nlbench.VerdictCorrect:
			marks[i] = "ok"
		case nlbench.VerdictWrong:
			marks[i] = "WRONG"
		case nlbench.VerdictFailed:
			marks[i] = "fail"
		default:
			marks[i] = "none"
		}
	}
	return strings.Join(marks, ", ")
}

func pathLabel(p string) string {
	if p == "raw" {
		return "Raw text-to-SQL"
	}
	return "Semantic layer (MCP)"
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
