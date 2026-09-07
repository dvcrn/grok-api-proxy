package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCurrentAccessTokenRefreshesBeforeExpiry(t *testing.T) {
	store := &memoryTokenStore{
		tokens: &AuthTokens{
			AccessToken:  "expiring",
			RefreshToken: "existing-refresh",
			ExpiresAt:    time.Now().Add(time.Minute).Unix(),
		},
	}
	calls := 0
	client := testHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read refresh request: %v", err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse refresh request: %v", err)
		}
		if req.URL.String() != tokenURL || form.Get("grant_type") != "refresh_token" || form.Get("client_id") != clientID || form.Get("refresh_token") != "existing-refresh" {
			t.Errorf("refresh request = %s %v", req.URL, form)
		}
		return jsonResponse(http.StatusOK, `{"access_token":"refreshed","expires_in":3600}`), nil
	})
	configureRuntime(store, client)

	tokens, err := currentAccessToken()
	if err != nil {
		t.Fatalf("currentAccessToken() error = %v", err)
	}
	if tokens.AccessToken != "refreshed" || tokens.RefreshToken != "existing-refresh" {
		t.Errorf("tokens = %+v", tokens)
	}
	if calls != 1 {
		t.Errorf("refresh calls = %d, want 1", calls)
	}
	if store.tokens == nil || store.tokens.AccessToken != "refreshed" {
		t.Errorf("saved tokens = %+v", store.tokens)
	}
}

func TestRequestTokensRejectsIncompleteResponse(t *testing.T) {
	configureRuntime(&memoryTokenStore{}, testHTTPClientFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"refresh_token":"refresh","expires_in":3600}`)),
		}, nil
	}))

	_, err := requestTokens(t.Context(), url.Values{"grant_type": {"refresh_token"}}, "refresh")
	if err == nil || !strings.Contains(err.Error(), "invalid token response") {
		t.Fatalf("requestTokens() error = %v", err)
	}
}
