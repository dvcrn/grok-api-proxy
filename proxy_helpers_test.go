package main

import (
	"net/http"
	"net/url"
	"testing"
)

func TestWorkerUpstreamURL(t *testing.T) {
	incoming, err := url.Parse("https://proxy.example/v1/chat/completions?key=admin-secret&foo=bar")
	if err != nil {
		t.Fatalf("parse incoming URL: %v", err)
	}
	got, err := workerUpstreamURL(incoming)
	if err != nil {
		t.Fatalf("workerUpstreamURL() error = %v", err)
	}
	want := "https://api.x.ai/v1/chat/completions?foo=bar"
	if got != want {
		t.Errorf("workerUpstreamURL() = %q, want %q", got, want)
	}
}

func TestCopyRequestHeadersStripsProxyMetadata(t *testing.T) {
	source := http.Header{
		"Content-Type":      {"application/json"},
		"X-Stainless-Lang":  {"go"},
		"Authorization":     {"Bearer admin-secret"},
		"X-Api-Key":         {"admin-secret"},
		"Cf-Connecting-Ip":  {"192.0.2.1"},
		"Cf-Ray":            {"ray"},
		"X-Forwarded-For":   {"192.0.2.1"},
		"Transfer-Encoding": {"chunked"},
	}
	destination := make(http.Header)
	copyRequestHeaders(destination, source)

	if destination.Get("Content-Type") != "application/json" || destination.Get("X-Stainless-Lang") != "go" {
		t.Errorf("expected client headers, got %v", destination)
	}
	for _, key := range []string{"Authorization", "X-Api-Key", "Cf-Connecting-Ip", "Cf-Ray", "X-Forwarded-For", "Transfer-Encoding"} {
		if destination.Get(key) != "" {
			t.Errorf("%s was forwarded", key)
		}
	}
}
