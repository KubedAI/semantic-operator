package nlbench

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// LLM wraps the Bedrock Converse API at temperature 0 for reproducibility.
type LLM struct {
	client  *bedrockruntime.Client
	ModelID string
}

// NewLLM builds the Bedrock client from the default AWS chain (IRSA or local
// credentials). ModelID is a Helm/env value, never hardcoded at call sites.
func NewLLM(ctx context.Context, region, modelID string) (*LLM, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &LLM{client: bedrockruntime.NewFromConfig(cfg), ModelID: modelID}, nil
}

// Complete runs a single-turn completion with no tools.
func (l *LLM) Complete(ctx context.Context, system, user string) (string, error) {
	out, err := l.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(l.ModelID),
		System:  []brtypes.SystemContentBlock{&brtypes.SystemContentBlockMemberText{Value: system}},
		Messages: []brtypes.Message{{
			Role:    brtypes.ConversationRoleUser,
			Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: user}},
		}},
		InferenceConfig: &brtypes.InferenceConfiguration{Temperature: aws.Float32(0)},
	})
	if err != nil {
		return "", err
	}
	return messageText(out), nil
}

// ToolDef is a provider-neutral tool definition (bridged from MCP).
type ToolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

// RunToolLoop drives a bounded agent loop: the model may call tools (served
// by the callback) until it produces a final text answer or maxTurns is hit.
// It returns the final answer and the ordered tool-call trace.
func (l *LLM) RunToolLoop(ctx context.Context, system, user string, tools []ToolDef,
	call func(context.Context, ToolCall) (string, error), maxTurns int) (string, []string, error) {
	var brTools []brtypes.Tool
	for _, t := range tools {
		brTools = append(brTools, &brtypes.ToolMemberToolSpec{Value: brtypes.ToolSpecification{
			Name:        aws.String(t.Name),
			Description: aws.String(t.Description),
			InputSchema: &brtypes.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(t.InputSchema)},
		}})
	}

	msgs := []brtypes.Message{{
		Role:    brtypes.ConversationRoleUser,
		Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: user}},
	}}
	var trace []string

	for turn := 0; turn < maxTurns; turn++ {
		out, err := l.client.Converse(ctx, &bedrockruntime.ConverseInput{
			ModelId:         aws.String(l.ModelID),
			System:          []brtypes.SystemContentBlock{&brtypes.SystemContentBlockMemberText{Value: system}},
			Messages:        msgs,
			ToolConfig:      &brtypes.ToolConfiguration{Tools: brTools},
			InferenceConfig: &brtypes.InferenceConfiguration{Temperature: aws.Float32(0)},
		})
		if err != nil {
			return "", trace, err
		}
		reply, ok := out.Output.(*brtypes.ConverseOutputMemberMessage)
		if !ok {
			return "", trace, fmt.Errorf("unexpected converse output %T", out.Output)
		}
		msgs = append(msgs, reply.Value)

		if out.StopReason != brtypes.StopReasonToolUse {
			return messageText(out), trace, nil
		}

		var results []brtypes.ContentBlock
		for _, block := range reply.Value.Content {
			tu, ok := block.(*brtypes.ContentBlockMemberToolUse)
			if !ok {
				continue
			}
			input := map[string]any{}
			if tu.Value.Input != nil {
				if b, err := tu.Value.Input.MarshalSmithyDocument(); err == nil {
					_ = json.Unmarshal(b, &input)
				}
			}
			tc := ToolCall{ID: aws.ToString(tu.Value.ToolUseId), Name: aws.ToString(tu.Value.Name), Input: input}
			trace = append(trace, tc.Name)
			text, err := call(ctx, tc)
			status := brtypes.ToolResultStatusSuccess
			if err != nil {
				text = "error: " + err.Error()
				status = brtypes.ToolResultStatusError
			}
			results = append(results, &brtypes.ContentBlockMemberToolResult{Value: brtypes.ToolResultBlock{
				ToolUseId: tu.Value.ToolUseId,
				Status:    status,
				Content:   []brtypes.ToolResultContentBlock{&brtypes.ToolResultContentBlockMemberText{Value: text}},
			}})
		}
		msgs = append(msgs, brtypes.Message{Role: brtypes.ConversationRoleUser, Content: results})
	}
	return "", trace, fmt.Errorf("tool loop exceeded %d turns", maxTurns)
}

func messageText(out *bedrockruntime.ConverseOutput) string {
	msg, ok := out.Output.(*brtypes.ConverseOutputMemberMessage)
	if !ok {
		return ""
	}
	var text string
	for _, c := range msg.Value.Content {
		if t, ok := c.(*brtypes.ContentBlockMemberText); ok {
			text += t.Value
		}
	}
	return text
}
