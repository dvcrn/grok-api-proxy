package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	clientID           = "b1a00492-073a-47ea-816f-4c329264a828"
	scope              = "openid profile email offline_access grok-cli:access api:access"
	deviceCodeURL      = "https://auth.x.ai/oauth2/device/code"
	tokenURL           = "https://auth.x.ai/oauth2/token"
	apiURL             = "https://api.x.ai/v1"
	refreshSkew        = 5 * time.Minute
	oauthTimeout       = 30 * time.Second
	oauthResponseLimit = 1 << 20
)

type AuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

type tokenStore interface {
	LoadTokens() (*AuthTokens, error)
	SaveTokens(AuthTokens) error
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

var (
	runtimeStore  tokenStore
	runtimeClient HTTPClient
	refreshMu     sync.Mutex
)

func configureRuntime(store tokenStore, client HTTPClient) {
	runtimeStore = store
	runtimeClient = client
}

func saveTokens(tokens AuthTokens) error {
	if runtimeStore == nil {
		return errors.New("token store is not configured")
	}
	return runtimeStore.SaveTokens(tokens)
}

func loadTokens() (*AuthTokens, error) {
	if runtimeStore == nil {
		return nil, errors.New("token store is not configured")
	}
	return runtimeStore.LoadTokens()
}

func refreshToken(refresh string) (*AuthTokens, error) {
	if strings.TrimSpace(refresh) == "" {
		return nil, errors.New("no refresh token available")
	}
	return requestTokens(context.Background(), url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refresh},
	}, refresh)
}

func requestTokens(ctx context.Context, form url.Values, previousRefreshToken string) (*AuthTokens, error) {
	if runtimeClient == nil {
		return nil, errors.New("OAuth HTTP client is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := runtimeClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("token request failed with status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, oauthResponseLimit))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	tokens, err := tokensFromJSON(body, previousRefreshToken, time.Now())
	if err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return tokens, nil
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

func writeJSONError(w http.ResponseWriter, status int, response errorResponse) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("write json error response: %v", err)
	}
}

var (
	unauthorizedResponse = errorResponse{
		Error:   "Unauthorized",
		Message: "Grok OAuth credentials are not configured.",
	}
	tokenExpiredResponse = errorResponse{
		Error:   "Token expired",
		Message: "Failed to refresh the Grok OAuth token.",
	}
	errNotAuthenticated   = errors.New("no stored credentials")
	errTokenRefreshFailed = errors.New("failed to refresh token")
)

func currentAccessToken() (*AuthTokens, error) {
	tokens, err := loadTokens()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotAuthenticated, err)
	}
	if tokens.AccessToken == "" {
		return nil, errNotAuthenticated
	}
	if tokens.ExpiresAt > time.Now().Add(refreshSkew).Unix() {
		return tokens, nil
	}

	refreshMu.Lock()
	defer refreshMu.Unlock()
	tokens, err = loadTokens()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotAuthenticated, err)
	}
	if tokens.ExpiresAt > time.Now().Add(refreshSkew).Unix() {
		return tokens, nil
	}
	return refreshAndSave(tokens)
}

func forceRefreshToken(failedAccessToken string) (*AuthTokens, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	tokens, err := loadTokens()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotAuthenticated, err)
	}
	if tokens.AccessToken != failedAccessToken && tokens.AccessToken != "" {
		return tokens, nil
	}
	return refreshAndSave(tokens)
}

func refreshAndSave(tokens *AuthTokens) (*AuthTokens, error) {
	newTokens, err := refreshToken(tokens.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errTokenRefreshFailed, err)
	}
	if err := saveTokens(*newTokens); err != nil {
		return nil, fmt.Errorf("%w: save refreshed tokens: %v", errTokenRefreshFailed, err)
	}
	return newTokens, nil
}

func ensureAccessToken(w http.ResponseWriter) (*AuthTokens, bool) {
	tokens, err := currentAccessToken()
	if errors.Is(err, errTokenRefreshFailed) {
		log.Printf("Failed to refresh token: %v", err)
		writeJSONError(w, http.StatusUnauthorized, tokenExpiredResponse)
		return nil, false
	}
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, unauthorizedResponse)
		return nil, false
	}
	return tokens, true
}
