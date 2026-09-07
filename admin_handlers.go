package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
)

func registerAdminRoutes(mux *http.ServeMux, auth *deviceAuth) {
	mux.HandleFunc("/admin/auth/start", adminMiddleware(deviceAuthStartHandler(auth)))
	mux.HandleFunc("/admin/auth/status", adminMiddleware(deviceAuthStatusHandler(auth)))
	mux.HandleFunc("/admin/tokens", adminMiddleware(tokensHandler))
	mux.HandleFunc("/admin/status", adminMiddleware(tokenStatusHandler))
}

func deviceAuthStartHandler(auth *deviceAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !adminRequestAllowed(w, r, http.MethodPost) || !discardLimitedRequestBody(w, r, 1024) {
			return
		}
		status, err := auth.Start(r.Context())
		writeDeviceAuthResponse(w, status, err)
	}
}

func deviceAuthStatusHandler(auth *deviceAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !adminRequestAllowed(w, r, http.MethodGet, http.MethodPost) {
			return
		}
		var (
			status deviceAuthStatus
			err    error
		)
		if r.Method == http.MethodGet {
			status, err = auth.Status()
		} else {
			if !discardLimitedRequestBody(w, r, 1024) {
				return
			}
			status, err = auth.Poll(r.Context())
		}
		writeDeviceAuthResponse(w, status, err)
	}
}

func tokensHandler(w http.ResponseWriter, r *http.Request) {
	if !adminRequestAllowed(w, r, http.MethodPost) || !requireJSONContentType(w, r) {
		return
	}
	body, ok := readLimitedRequestBody(w, r, 65536)
	if !ok {
		return
	}
	var input struct {
		AccessToken  string  `json:"accessToken"`
		RefreshToken *string `json:"refreshToken,omitempty"`
		ExpiresAt    int64   `json:"expiresAt"`
	}
	if err := decodeStrictJSON(body, &input); err != nil || strings.TrimSpace(input.AccessToken) == "" || input.ExpiresAt < 1_000_000_000_000 || emptyOptional(input.RefreshToken) {
		writeAdminError(w, "Expected accessToken, expiresAt, and optional refreshToken", http.StatusBadRequest)
		return
	}
	refresh := ""
	if input.RefreshToken != nil {
		refresh = strings.TrimSpace(*input.RefreshToken)
	} else if current, err := loadTokens(); err == nil {
		refresh = current.RefreshToken
	}
	if refresh == "" {
		writeAdminError(w, "Expected refreshToken when no refresh token is stored", http.StatusBadRequest)
		return
	}
	tokens := AuthTokens{
		AccessToken:  strings.TrimSpace(input.AccessToken),
		RefreshToken: refresh,
		ExpiresAt:    input.ExpiresAt / 1000,
	}
	if tokens.ExpiresAt <= 0 {
		writeAdminError(w, "Expected expiresAt in Unix milliseconds", http.StatusBadRequest)
		return
	}
	if err := saveTokens(tokens); err != nil {
		log.Printf("Failed to store OAuth credentials: %v", err)
		writeAdminError(w, "Failed to store credentials", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, map[string]bool{"stored": true})
}

func tokenStatusHandler(w http.ResponseWriter, r *http.Request) {
	if !adminRequestAllowed(w, r, http.MethodGet) {
		return
	}
	tokens, err := loadTokens()
	writeAdminJSON(w, map[string]bool{
		"configured": err == nil && tokens != nil && tokens.AccessToken != "",
	})
}

func adminRequestAllowed(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	allowed := false
	for _, method := range methods {
		if r.Method == method {
			allowed = true
			break
		}
	}
	if !allowed {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeAdminError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	scheme := r.URL.Scheme
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	if origin != scheme+"://"+r.Host {
		writeAdminError(w, "Origin is not allowed", http.StatusForbidden)
		return false
	}
	return true
}

func requireJSONContentType(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAdminError(w, "Expected application/json", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

func emptyOptional(value *string) bool {
	return value != nil && strings.TrimSpace(*value) == ""
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func discardLimitedRequestBody(w http.ResponseWriter, r *http.Request, limit int64) bool {
	_, ok := readLimitedRequestBody(w, r, limit)
	return ok
}

func readLimitedRequestBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		writeAdminError(w, "Invalid request body", http.StatusBadRequest)
		return nil, false
	}
	if int64(len(body)) > limit {
		writeAdminError(w, "Request body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return body, true
}

func writeDeviceAuthResponse(w http.ResponseWriter, status deviceAuthStatus, err error) {
	if err != nil {
		var authErr *deviceAuthError
		if errors.As(err, &authErr) {
			writeAdminError(w, authErr.Error(), http.StatusBadGateway)
			return
		}
		log.Printf("Device authorization request failed: %v", err)
		writeAdminError(w, "Device authorization request failed", http.StatusBadGateway)
		return
	}
	writeAdminJSON(w, status)
}

func writeAdminJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeAdminError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.Error(w, message, status)
}
