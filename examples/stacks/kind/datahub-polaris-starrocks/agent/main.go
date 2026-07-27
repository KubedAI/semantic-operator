// Command agent is an interactive host-side analyst for the datahub-polaris
// demo. It drives the Semantic Operator MCP and (optionally) the DataHub MCP
// through Amazon Bedrock's OpenAI-compatible Chat Completions endpoint. The
// model never writes SQL; it selects certified metrics and reads catalog
// metadata through governed tools.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	role := flag.String("role", env("SEMANTIC_ROLE", "platform_analyst"),
		"caller role sent as X-Semantic-Role (e.g. platform_analyst, finance_analyst, na_customer_success)")
	model := flag.String("model", env("BEDROCK_MODEL_ID", os.Getenv("OPENAI_MODEL")),
		"model id for the Chat Completions endpoint")
	question := flag.String("question", "", "ask one question and exit (non-interactive)")
	operatorEndpoint := flag.String("operator-mcp", env("SEMANTIC_MCP_URL", "http://localhost:8090/mcp"),
		"Semantic Operator MCP endpoint")
	datahubEndpoint := flag.String("datahub-mcp", env("DATAHUB_MCP_URL", ""),
		"DataHub MCP endpoint (optional; empty = operator only)")
	maxIters := flag.Int("max-iters", 12, "maximum tool calls per question")
	flag.Parse()

	baseURL := os.Getenv("OPENAI_BASE_URL")
	apiKey := os.Getenv("OPENAI_API_KEY")
	if baseURL == "" || apiKey == "" {
		fatal("set OPENAI_BASE_URL and OPENAI_API_KEY (for Bedrock, the bedrock-mantle endpoint and a Bedrock API key)")
	}
	if *model == "" {
		fatal("set --model or BEDROCK_MODEL_ID to the model id to use")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dhToken := env("DATAHUB_GMS_TOKEN", "local-no-auth")
	mcp, tools, err := NewMCP(ctx, *operatorEndpoint, *role, *datahubEndpoint, dhToken)
	if err != nil {
		fatal(err.Error())
	}
	defer mcp.Close()

	llm := NewLLM(baseURL, apiKey, *model)
	agent := NewAgent(llm, systemPrompt, tools, mcp.Call, *maxIters)

	fmt.Printf("connected: %d tools, role=%s, model=%s\n", len(tools), *role, *model)
	if *datahubEndpoint == "" {
		fmt.Println("note: DataHub MCP not set (--datahub-mcp / DATAHUB_MCP_URL); running operator-only.")
	}

	if *question != "" {
		answer(ctx, agent, mcp, *question)
		return
	}
	repl(ctx, agent, mcp, *role)
}

// repl reads questions until EOF/Ctrl-D or an interrupt. Conversation memory
// is retained across turns by the Agent.
func repl(ctx context.Context, agent *Agent, mcp *MCP, role string) {
	fmt.Printf("\nInteractive agent (role=%s). Ask a question; Ctrl-D or /quit to exit.\n", role)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Print("\n> ")
		if !sc.Scan() {
			fmt.Println()
			return
		}
		q := strings.TrimSpace(sc.Text())
		if q == "" {
			continue
		}
		if q == "/quit" || q == "/exit" {
			return
		}
		if ctx.Err() != nil {
			return
		}
		answer(ctx, agent, mcp, q)
	}
}

// answer runs one question and prints the reply, the tool trace, and any
// captured query provenance.
func answer(ctx context.Context, agent *Agent, mcp *MCP, q string) {
	mcp.LastProvenance = nil
	reply, err := agent.Ask(ctx, q)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return
	}
	fmt.Printf("\n%s\n", reply)
	if len(agent.Trace) > 0 {
		fmt.Printf("\n[tools: %s]\n", strings.Join(agent.Trace, ", "))
	}
	if p := mcp.LastProvenance; p != nil {
		fmt.Printf("[provenance: model=%s version=%s request=%s rows=%d]\n",
			p.Model, p.ModelVersion, p.RequestHash, p.RowCount)
		if p.SQL != "" {
			fmt.Printf("[sql] %s\n", p.SQL)
		}
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "agent: "+msg)
	os.Exit(1)
}
