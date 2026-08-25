package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testAdminKey = "test-admin-key"

// newMCPTestMux mounts /mcp exactly the way main does, so the tests exercise
// the admin middleware and the MCP handler together.
func newMCPTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	t.Setenv("ADMIN_API_KEY", testAdminKey)

	mux := http.NewServeMux()
	handler := adminMiddleware(mcpHandler())
	mux.HandleFunc("/mcp", handler)
	mux.HandleFunc("/mcp/", handler)
	return mux
}

// postMCP sends one JSON-RPC message to /mcp and returns the decoded response.
func postMCP(t *testing.T, mux *http.ServeMux, payload map[string]interface{}, authorized bool) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if authorized {
		req.Header.Set("Authorization", "Bearer "+testAdminKey)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		return rec, nil
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, rec.Body.String())
	}
	return rec, decoded
}

func mcpResult(t *testing.T, resp map[string]interface{}) map[string]interface{} {
	t.Helper()
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected a result object, got %v", resp)
	}
	return result
}

func TestMCPEndpointRequiresAdminKey(t *testing.T) {
	mux := newMCPTestMux(t)

	rec, _ := postMCP(t, mux, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}, false)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMCPEndpointRejectsWrongAdminKey(t *testing.T) {
	mux := newMCPTestMux(t)

	body, err := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer not-the-key")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMCPInitialize(t *testing.T) {
	mux := newMCPTestMux(t)

	rec, resp := postMCP(t, mux, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]interface{}{"name": "test-client", "version": "0.0.1"},
		},
	}, true)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	serverInfo, ok := mcpResult(t, resp)["serverInfo"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected serverInfo, got %v", resp)
	}
	if serverInfo["name"] != mcpServerName {
		t.Errorf("expected name %q, got %v", mcpServerName, serverInfo["name"])
	}
	if serverInfo["version"] != mcpServerVersion {
		t.Errorf("expected version %q, got %v", mcpServerVersion, serverInfo["version"])
	}
}

func TestMCPToolsList(t *testing.T) {
	mux := newMCPTestMux(t)

	rec, resp := postMCP(t, mux, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}, true)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	rawTools, ok := mcpResult(t, resp)["tools"].([]interface{})
	if !ok {
		t.Fatalf("expected tools array, got %v", resp)
	}

	tools := make(map[string]map[string]interface{}, len(rawTools))
	for _, rawTool := range rawTools {
		tool, ok := rawTool.(map[string]interface{})
		if !ok {
			t.Fatalf("expected tool object, got %v", rawTool)
		}
		name, _ := tool["name"].(string)
		tools[name] = tool
	}

	for _, name := range []string{"ask_grok", "ask_grok_models"} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("expected tool %s in %v", name, tools)
		}
		// Both descriptions must tell the caller the answers come from xAI.
		description, _ := tools[name]["description"].(string)
		if !bytes.Contains([]byte(description), []byte("xAI API")) {
			t.Errorf("tool %s description does not mention the xAI API: %q", name, description)
		}
	}

	schema, ok := tools["ask_grok"]["inputSchema"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected ask_grok inputSchema, got %v", tools["ask_grok"])
	}
	required, ok := schema["required"].([]interface{})
	if !ok || len(required) != 2 {
		t.Fatalf("expected two required properties, got %v", schema["required"])
	}
	got := map[string]bool{}
	for _, name := range required {
		got[name.(string)] = true
	}
	if !got["model"] || !got["prompt"] {
		t.Errorf("expected model and prompt to be required, got %v", required)
	}
}

func TestMCPAskGrokRejectsBlankInput(t *testing.T) {
	mux := newMCPTestMux(t)

	testCases := []struct {
		name        string
		args        map[string]interface{}
		wantMessage string
	}{
		{
			name:        "blank prompt",
			args:        map[string]interface{}{"model": "grok-code-fast-1", "prompt": "   "},
			wantMessage: "prompt is required",
		},
		{
			name:        "blank model",
			args:        map[string]interface{}{"model": "  ", "prompt": "hello"},
			wantMessage: "model is required",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rec, resp := postMCP(t, mux, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "tools/call",
				"params":  map[string]interface{}{"name": "ask_grok", "arguments": tc.args},
			}, true)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			result := mcpResult(t, resp)

			// Tool-level failures come back as isError, not a JSON-RPC error.
			if result["isError"] != true {
				t.Errorf("expected isError, got %v", result)
			}
			if text := mcpResultText(t, result); !bytes.Contains([]byte(text), []byte(tc.wantMessage)) {
				t.Errorf("expected %q in %q", tc.wantMessage, text)
			}
		})
	}
}

func mcpResultText(t *testing.T, result map[string]interface{}) string {
	t.Helper()

	content, ok := result["content"].([]interface{})
	if !ok {
		t.Fatalf("expected content array, got %v", result)
	}

	var text string
	for _, rawPart := range content {
		part, ok := rawPart.(map[string]interface{})
		if !ok {
			t.Fatalf("expected content part object, got %v", rawPart)
		}
		if partText, ok := part["text"].(string); ok {
			text += partText
		}
	}
	return text
}

func TestExtractCompletionText(t *testing.T) {
	var joined chatCompletionResponse
	if err := json.Unmarshal([]byte(`{"choices":[
		{"message":{"content":"Hello, "}},
		{"message":{"content":"world!"}}
	]}`), &joined); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	testCases := []struct {
		name       string
		completion *chatCompletionResponse
		want       string
	}{
		{name: "nil completion", completion: nil, want: ""},
		{name: "no choices", completion: &chatCompletionResponse{}, want: ""},
		{name: "joins choice content", completion: &joined, want: "Hello, world!"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractCompletionText(tc.completion); got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

// TestMCPAllowsNonLoopbackHost guards the DisableLocalhostProtection option.
// The SDK only applies DNS rebinding protection when the request carries a
// loopback http.LocalAddrContextKey, which a real listener sets and
// httptest.NewRequest does not - so this has to go through httptest.NewServer
// to exercise the check at all.
func TestMCPAllowsNonLoopbackHost(t *testing.T) {
	mux := newMCPTestMux(t)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+testAdminKey)
	// A public hostname forwarded by a tunnel, which the rebinding check would
	// otherwise reject with 403 because the listener itself is loopback.
	req.Host = "proxy.example.com"

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", resp.StatusCode, respBody)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if tools, _ := mcpResult(t, decoded)["tools"].([]interface{}); len(tools) == 0 {
		t.Fatalf("expected tools in %v", decoded)
	}
}
