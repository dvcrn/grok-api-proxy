package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeviceAuthHandlers(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", testAdminKey)
	store := &memoryTokenStore{}
	configureRuntime(store, testHTTPClientFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://auth.x.ai/activate","expires_in":900,"interval":5}`), nil
	}))
	auth := newDeviceAuth(store, runtimeClient)
	mux := http.NewServeMux()
	registerAdminRoutes(mux, auth)

	unauthorized := httptest.NewRequest(http.MethodPost, "/admin/auth/start", nil)
	unauthorizedResponse := httptest.NewRecorder()
	mux.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedResponse.Code)
	}

	startResponse := performAdminRequest(mux, http.MethodPost, "/admin/auth/start", "", "")
	if startResponse.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", startResponse.Code, startResponse.Body.String())
	}
	var started deviceAuthStatus
	if err := json.NewDecoder(startResponse.Body).Decode(&started); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if started.Status != "pending" || started.UserCode != "ABCD-EFGH" {
		t.Errorf("start response = %+v", started)
	}

	statusResponse := performAdminRequest(mux, http.MethodGet, "/admin/auth/status", "", "")
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"status":"pending"`) {
		t.Errorf("status = %d, body = %s", statusResponse.Code, statusResponse.Body.String())
	}
}

func TestTokensAndStatusHandlers(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", testAdminKey)
	store := &memoryTokenStore{}
	configureRuntime(store, http.DefaultClient)
	auth := newDeviceAuth(store, runtimeClient)
	mux := http.NewServeMux()
	registerAdminRoutes(mux, auth)

	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	body := `{"accessToken":"manual-access","refreshToken":"manual-refresh","expiresAt":` + number(expiresAt) + `}`
	response := performAdminRequest(mux, http.MethodPost, "/admin/tokens", body, "application/json")
	if response.Code != http.StatusOK {
		t.Fatalf("tokens status = %d, body = %s", response.Code, response.Body.String())
	}
	if store.tokens == nil || store.tokens.AccessToken != "manual-access" || store.tokens.RefreshToken != "manual-refresh" || store.tokens.ExpiresAt != expiresAt/1000 {
		t.Errorf("stored tokens = %+v", store.tokens)
	}

	status := performAdminRequest(mux, http.MethodGet, "/admin/status", "", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"configured":true`) {
		t.Errorf("status = %d, body = %s", status.Code, status.Body.String())
	}
	if got := status.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestTokensHandlerRejectsUnknownFields(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", testAdminKey)
	store := &memoryTokenStore{}
	configureRuntime(store, http.DefaultClient)
	mux := http.NewServeMux()
	registerAdminRoutes(mux, newDeviceAuth(store, runtimeClient))
	body := `{"accessToken":"access","refreshToken":"refresh","expiresAt":1,"unknown":true}`
	response := performAdminRequest(mux, http.MethodPost, "/admin/tokens", body, "application/json")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func performAdminRequest(handler http.Handler, method, target, body, contentType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testAdminKey)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
