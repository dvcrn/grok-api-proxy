package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestMergeModelsList_appendsExtra(t *testing.T) {
	upstream := []byte(`{
  "object": "list",
  "data": [
    {"id": "grok-4.3", "object": "model", "owned_by": "xai"}
  ]
}`)

	merged, err := mergeModelsList(upstream, extraModels)
	if err != nil {
		t.Fatalf("mergeModelsList: %v", err)
	}

	list := decodeModelsList(t, merged)
	if len(list.Data) != 2 {
		t.Fatalf("expected 2 models, got %d", len(list.Data))
	}

	ids := modelIDs(list)
	if !ids["grok-4.3"] {
		t.Fatal("expected upstream model grok-4.3 preserved")
	}
	if !ids["grok-composer-2.5-fast"] {
		t.Fatal("expected grok-composer-2.5-fast appended")
	}
}

func TestMergeModelsList_skipsDuplicateID(t *testing.T) {
	upstream := []byte(`{
  "object": "list",
  "data": [
    {"id": "grok-composer-2.5-fast", "object": "model", "owned_by": "xai"}
  ]
}`)

	merged, err := mergeModelsList(upstream, extraModels)
	if err != nil {
		t.Fatalf("mergeModelsList: %v", err)
	}

	list := decodeModelsList(t, merged)
	if len(list.Data) != 1 {
		t.Fatalf("expected 1 model (no duplicate), got %d", len(list.Data))
	}
}

func TestIsModelsListRequest(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{
			name:   "models path",
			method: http.MethodGet,
			path:   "/models",
			want:   true,
		},
		{
			name:   "versioned models path",
			method: http.MethodGet,
			path:   "/v1/models",
			want:   true,
		},
		{
			name:   "versioned models path with trailing slash",
			method: http.MethodGet,
			path:   "/v1/models/",
			want:   true,
		},
		{
			name:   "post models path",
			method: http.MethodPost,
			path:   "/models",
			want:   false,
		},
		{
			name:   "chat completions path",
			method: http.MethodGet,
			path:   "/chat/completions",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := http.NewRequest(tt.method, "http://127.0.0.1:56121"+tt.path, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}

			if got := isModelsListRequest(r); got != tt.want {
				t.Errorf(
					"isModelsListRequest(%s %s) = %v, want %v",
					tt.method,
					tt.path,
					got,
					tt.want,
				)
			}
		})
	}
}

func TestExtraModelsValid(t *testing.T) {
	for i, m := range extraModels {
		if m.ID == "" {
			t.Fatalf("extra model %d missing id", i)
		}
		if m.Object == "" {
			t.Fatalf("extra model %d missing object", i)
		}
	}
}

func decodeModelsList(t *testing.T, body []byte) modelsListResponse {
	t.Helper()
	var list modelsListResponse
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&list); err != nil {
		t.Fatalf("decode models list: %v", err)
	}
	return list
}

func modelIDs(list modelsListResponse) map[string]bool {
	ids := make(map[string]bool, len(list.Data))
	for _, raw := range list.Data {
		if id := modelIDFromRaw(raw); id != "" {
			ids[id] = true
		}
	}
	return ids
}
