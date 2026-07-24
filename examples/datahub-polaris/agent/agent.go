package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// Agent holds one conversation. It keeps the Responses input list across REPL
// turns (multi-turn memory) and runs a bounded tool loop for each question.
type Agent struct {
	llm      *LLM
	tools    []responses.ToolUnionParam
	exec     ToolExecutor
	instr    string
	input    responses.ResponseInputParam
	maxIters int
	// Trace records tool calls made during the most recent Ask, for display.
	Trace []string
}

// NewAgent seeds the conversation with the system instructions and the merged
// tool set. maxIters bounds tool calls per question so a confused model can't
// loop forever.
func NewAgent(llm *LLM, system string, tools []Tool, exec ToolExecutor, maxIters int) *Agent {
	toolParams := make([]responses.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object"}
		}
		fn := responses.ToolParamOfFunction(t.Name, params, false)
		if t.Description != "" && fn.OfFunction != nil {
			fn.OfFunction.Description = openai.String(t.Description)
		}
		toolParams = append(toolParams, fn)
	}
	return &Agent{
		llm:      llm,
		tools:    toolParams,
		exec:     exec,
		instr:    system,
		maxIters: maxIters,
	}
}

// Ask runs one question to completion: the model may emit function calls
// repeatedly, each result is fed back as a function_call_output, and the loop
// ends when the model responds with no further calls (or the budget is
// exhausted). The input list is retained for the next Ask.
func (a *Agent) Ask(ctx context.Context, question string) (string, error) {
	a.Trace = nil
	a.input = append(a.input, responses.ResponseInputItemUnionParam{
		OfMessage: &responses.EasyInputMessageParam{
			Role:    responses.EasyInputMessageRoleUser,
			Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(question)},
		},
	})

	for i := 0; i < a.maxIters; i++ {
		resp, err := a.llm.client.Responses.New(ctx, responses.ResponseNewParams{
			Model:        shared.ResponsesModel(a.llm.model),
			Instructions: openai.String(a.instr),
			Input:        responses.ResponseNewParamsInputUnion{OfInputItemList: a.input},
			Tools:        a.tools,
		})
		if err != nil {
			return "", err
		}

		var calls []responses.ResponseFunctionToolCall
		for _, item := range resp.Output {
			if fc := item.AsFunctionCall(); fc.CallID != "" {
				calls = append(calls, fc)
			}
		}

		if len(calls) == 0 {
			return strings.TrimSpace(resp.OutputText()), nil
		}

		for _, fc := range calls {
			a.Trace = append(a.Trace, fmt.Sprintf("%s(%s)", fc.Name, truncate(fc.Arguments, 200)))
			// Echo the model's function call into the conversation, then feed
			// its result back as a function_call_output for the next turn.
			a.input = append(a.input, responses.ResponseInputItemParamOfFunctionCall(fc.Arguments, fc.CallID, fc.Name))
			result, err := a.exec(ctx, fc.Name, json.RawMessage(fc.Arguments))
			if err != nil {
				result = fmt.Sprintf("ERROR: %v", err)
			}
			a.input = append(a.input, responses.ResponseInputItemParamOfFunctionCallOutput(fc.CallID, result))
		}
	}
	return "", fmt.Errorf("reached tool-call limit (%d) without a final answer", a.maxIters)
}
