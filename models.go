package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

type model struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

var extraModels = []model{
	{
		ID:      "grok-composer-2.5-fast",
		Created: 1776384000,
		Object:  "model",
		OwnedBy: "xai",
	},
}

type modelsListResponse struct {
	Object string            `json:"object"`
	Data   []json.RawMessage `json:"data"`
}

func isModelsListRequest(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	return path == "/models" || path == "/v1/models"
}

func modelIDFromRaw(raw json.RawMessage) string {
	var m struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	return m.ID
}

func mergeModelsList(upstreamBody []byte, extras []model) ([]byte, error) {
	var list modelsListResponse
	if err := json.Unmarshal(upstreamBody, &list); err != nil {
		return nil, fmt.Errorf("parse upstream models: %w", err)
	}
	if list.Object == "" {
		list.Object = "list"
	}

	seen := make(map[string]struct{}, len(list.Data)+len(extras))
	for _, raw := range list.Data {
		if id := modelIDFromRaw(raw); id != "" {
			seen[id] = struct{}{}
		}
	}

	for _, extra := range extras {
		if _, ok := seen[extra.ID]; ok {
			continue
		}

		raw, err := json.Marshal(extra)
		if err != nil {
			return nil, fmt.Errorf("marshal extra model %q: %w", extra.ID, err)
		}
		list.Data = append(list.Data, raw)
		seen[extra.ID] = struct{}{}
	}

	merged, err := json.Marshal(list)
	if err != nil {
		return nil, fmt.Errorf("marshal merged models: %w", err)
	}
	return merged, nil
}

func fetchUpstreamModels(r *http.Request, accessToken string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, apiURL+"/models", nil)
	if err != nil {
		return 0, nil, fmt.Errorf("create upstream models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("fetch upstream models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read upstream models response: %w", err)
	}
	return resp.StatusCode, body, nil
}

func handleModelsList(w http.ResponseWriter, r *http.Request, tokens *AuthTokens) {
	status, body, err := fetchUpstreamModels(r, tokens.AccessToken)
	if err != nil {
		log.Printf("upstream models fetch failed: %v", err)
		writeJSONError(
			w,
			http.StatusBadGateway,
			errorResponse{Error: "Failed to fetch models from upstream"},
		)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}

	merged, err := mergeModelsList(body, extraModels)
	if err != nil {
		log.Printf("merge models failed: %v", err)
		_, _ = w.Write(body)
		return
	}

	_, _ = w.Write(merged)
}
