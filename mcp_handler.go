package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpServerName    = "ask-grok"
	mcpServerVersion = "0.1.0"
)

// askGrokInput is the input for the ask_grok tool.
type askGrokInput struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// askGrokOutput is the structured result of the ask_grok tool. Model is the
// model that actually served the request, as reported by the xAI API.
type askGrokOutput struct {
	RequestedModel string `json:"requested_model"`
	Model          string `json:"model"`
	Text           string `json:"text"`
}

// askGrokModelsInput is the (empty) input for the ask_grok_models tool.
type askGrokModelsInput struct{}

type askGrokModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

type askGrokModelsOutput struct {
	Models []askGrokModel `json:"models"`
}

// chatCompletionResponse is the subset of the xAI chat completions response the
// ask_grok tool needs.
type chatCompletionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// newMCPServer builds the MCP server exposed at /mcp. Tools call the xAI API
// directly with the stored OAuth credentials, the same ones the reverse proxy
// injects into forwarded requests.
func newMCPServer() *mcpsdk.Server {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    mcpServerName,
		Version: mcpServerVersion,
	}, nil)

	addMCPTool(srv, &mcpsdk.Tool{
		Name: "ask_grok",
		Description: "Ask a Grok model a single self-contained question and get its answer back as text. " +
			"Requests are served by the xAI API using the local Grok CLI OAuth credentials, so no " +
			"separate xAI API key is involved. Each call is one-shot: there is no conversation history, " +
			"so put everything the model needs into the prompt. Call ask_grok_models first if you are " +
			"unsure which model IDs are available.",
		InputSchema: mcpObjectSchema(map[string]any{
			"model": mcpStringSchema("Model ID to ask, e.g. grok-code-fast-1. " +
				"Use ask_grok_models to list the IDs the xAI API currently offers."),
			"prompt": mcpStringSchema("The full question or instruction to send to the model."),
		}, "model", "prompt"),
	}, mcpAskGrok)

	addMCPTool(srv, &mcpsdk.Tool{
		Name: "ask_grok_models",
		Description: "List the model IDs that can be passed to ask_grok. Models come from the xAI API and " +
			"reflect what the current Grok account is entitled to, so the list can differ between " +
			"accounts and change over time.",
		InputSchema: mcpObjectSchema(map[string]any{}),
	}, mcpAskGrokModels)

	return srv
}

// mcpHandler returns the stateless streamable HTTP handler for /mcp. The server
// keeps no per-session state, so every request is served with a fresh session
// and plain JSON responses instead of an SSE stream.
func mcpHandler() http.HandlerFunc {
	mcpServer := newMCPServer()
	handler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return mcpServer },
		&mcpsdk.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			// The SDK auto-enables DNS rebinding protection when the listener is
			// loopback, rejecting any request whose Host is not also loopback. The
			// proxy is served on a public hostname through a tunnel that forwards
			// the original Host, so that check rejects every remote client. Access
			// is already gated by the admin bearer token in adminMiddleware.
			DisableLocalhostProtection: true,
		},
	)
	return handler.ServeHTTP
}

func mcpAskGrok(ctx context.Context, in askGrokInput) (askGrokOutput, error) {
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return askGrokOutput{}, fmt.Errorf("prompt is required")
	}

	requestedModel := strings.TrimSpace(in.Model)
	if requestedModel == "" {
		return askGrokOutput{}, fmt.Errorf("model is required; call ask_grok_models to list available models")
	}

	tokens, err := currentAccessToken()
	if err != nil {
		return askGrokOutput{}, fmt.Errorf("not authenticated: %w", err)
	}

	body, err := json.Marshal(map[string]interface{}{
		"model": requestedModel,
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": prompt},
		},
		"stream": false,
	})
	if err != nil {
		return askGrokOutput{}, fmt.Errorf("failed to prepare request for model %q: %w", requestedModel, err)
	}

	log.Printf("MCP ask_grok request received: model=%s prompt_len=%d", requestedModel, len(prompt))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return askGrokOutput{}, fmt.Errorf("failed to create upstream request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	apiCallStart := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("MCP ask_grok upstream request failed after %s: %v", time.Since(apiCallStart), err)
		return askGrokOutput{}, fmt.Errorf("ask_grok failed for model %q: %w", requestedModel, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return askGrokOutput{}, fmt.Errorf("ask_grok failed for model %q: read upstream response: %w", requestedModel, err)
	}

	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(respBody))
		if len(detail) > 2048 {
			detail = detail[:2048]
		}
		log.Printf("MCP ask_grok upstream returned %d for model %s", resp.StatusCode, requestedModel)
		return askGrokOutput{}, fmt.Errorf("ask_grok failed for model %q: upstream returned %d: %s",
			requestedModel, resp.StatusCode, detail)
	}

	var completion chatCompletionResponse
	if err := json.Unmarshal(respBody, &completion); err != nil {
		return askGrokOutput{}, fmt.Errorf("ask_grok failed for model %q: parse upstream response: %w", requestedModel, err)
	}

	// The API echoes back the model it served, which can differ from the ID we
	// asked for when xAI aliases a model.
	servedModel := completion.Model
	if servedModel == "" {
		servedModel = requestedModel
	}

	text := extractCompletionText(&completion)
	if text == "" {
		return askGrokOutput{}, fmt.Errorf("model %q returned no text", servedModel)
	}

	log.Printf("MCP ask_grok completed: model=%s text_len=%d duration=%s", servedModel, len(text), time.Since(apiCallStart))

	return askGrokOutput{
		RequestedModel: requestedModel,
		Model:          servedModel,
		Text:           text,
	}, nil
}

func mcpAskGrokModels(ctx context.Context, _ askGrokModelsInput) (askGrokModelsOutput, error) {
	tokens, err := currentAccessToken()
	if err != nil {
		return askGrokModelsOutput{}, fmt.Errorf("not authenticated: %w", err)
	}

	status, body, err := fetchModels(ctx, tokens.AccessToken)
	if err != nil {
		log.Printf("MCP ask_grok_models failed to fetch available models: %v", err)
		return askGrokModelsOutput{}, fmt.Errorf("failed to fetch available models: %w", err)
	}
	if status != http.StatusOK {
		return askGrokModelsOutput{}, fmt.Errorf("failed to fetch available models: upstream returned %d", status)
	}

	merged, err := mergeModelsList(body, extraModels)
	if err != nil {
		log.Printf("MCP ask_grok_models failed to merge extra models: %v", err)
		merged = body
	}

	var list modelsListResponse
	if err := json.Unmarshal(merged, &list); err != nil {
		return askGrokModelsOutput{}, fmt.Errorf("failed to parse available models: %w", err)
	}

	models := make([]askGrokModel, 0, len(list.Data))
	for _, raw := range list.Data {
		var entry askGrokModel
		if err := json.Unmarshal(raw, &entry); err != nil || entry.ID == "" {
			continue
		}
		models = append(models, entry)
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return askGrokModelsOutput{Models: models}, nil
}

// extractCompletionText joins the assistant text across the returned choices.
func extractCompletionText(completion *chatCompletionResponse) string {
	if completion == nil {
		return ""
	}

	var b strings.Builder
	for _, choice := range completion.Choices {
		b.WriteString(choice.Message.Content)
	}
	return b.String()
}

// addMCPTool registers a tool whose handler returns a structured result, which
// the SDK marshals into both the structured content and the text fallback.
func addMCPTool[In, Out any](srv *mcpsdk.Server, tool *mcpsdk.Tool, handler func(context.Context, In) (Out, error)) {
	mcpsdk.AddTool(srv, tool, func(ctx context.Context, _ *mcpsdk.CallToolRequest, input In) (*mcpsdk.CallToolResult, Out, error) {
		output, err := handler(ctx, input)
		return nil, output, err
	})
}

func mcpObjectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func mcpStringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
