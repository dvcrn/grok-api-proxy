package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const deviceAuthTimeout = 30 * time.Second

type deviceAuthStore interface {
	tokenStore
	LoadDeviceAuthSession() ([]byte, error)
	SaveDeviceAuthSession([]byte) error
	CompleteDeviceAuth(AuthTokens) error
}

type deviceAuthError struct {
	message string
}

func (e *deviceAuthError) Error() string {
	return e.message
}

type deviceAuth struct {
	store  deviceAuthStore
	client HTTPClient
	now    func() time.Time
	mu     sync.Mutex
}

type deviceAuthSession struct {
	Status                  string `json:"status"`
	DeviceCode              string `json:"deviceCode,omitempty"`
	UserCode                string `json:"userCode,omitempty"`
	VerificationURI         string `json:"verificationUri,omitempty"`
	VerificationURIComplete string `json:"verificationUriComplete,omitempty"`
	ExpiresAt               int64  `json:"expiresAt,omitempty"`
	IntervalMS              int64  `json:"intervalMs,omitempty"`
	NextPollAt              int64  `json:"nextPollAt,omitempty"`
}

type deviceAuthStatus struct {
	Status            string `json:"status"`
	VerificationURL   string `json:"verificationUrl,omitempty"`
	UserCode          string `json:"userCode,omitempty"`
	ExpiresAt         string `json:"expiresAt,omitempty"`
	RetryAfterSeconds int64  `json:"retryAfterSeconds,omitempty"`
}

func newDeviceAuth(store deviceAuthStore, client HTTPClient) *deviceAuth {
	return &deviceAuth{store: store, client: client, now: time.Now}
}

func (a *deviceAuth) Start(ctx context.Context) (deviceAuthStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	current, err := a.status()
	if err != nil {
		return deviceAuthStatus{}, err
	}
	if current.Status == "pending" {
		return current, nil
	}

	response, body, err := a.postForm(ctx, deviceCodeURL, url.Values{
		"client_id": {clientID},
		"scope":     {scope},
		"referrer":  {"pi"},
	})
	if err != nil {
		return deviceAuthStatus{}, err
	}
	if response < http.StatusOK || response >= http.StatusMultipleChoices {
		return deviceAuthStatus{}, &deviceAuthError{message: fmt.Sprintf("Device authorization could not be started (HTTP %d)", response)}
	}
	var started struct {
		DeviceCode              string  `json:"device_code"`
		UserCode                string  `json:"user_code"`
		VerificationURI         string  `json:"verification_uri"`
		VerificationURIComplete string  `json:"verification_uri_complete"`
		ExpiresIn               float64 `json:"expires_in"`
		Interval                float64 `json:"interval"`
	}
	if err := json.Unmarshal(body, &started); err != nil || started.DeviceCode == "" || started.UserCode == "" || started.ExpiresIn <= 0 {
		return deviceAuthStatus{}, &deviceAuthError{message: "Invalid device authorization response"}
	}
	verificationURI, err := trustedVerificationURL(started.VerificationURI)
	if err != nil {
		return deviceAuthStatus{}, &deviceAuthError{message: err.Error()}
	}
	verificationComplete := ""
	if started.VerificationURIComplete != "" {
		verificationComplete, err = trustedVerificationURL(started.VerificationURIComplete)
		if err != nil {
			return deviceAuthStatus{}, &deviceAuthError{message: err.Error()}
		}
	}
	interval := time.Duration(started.Interval * float64(time.Second))
	if interval <= 0 {
		interval = 5 * time.Second
	}
	now := a.now()
	session := deviceAuthSession{
		Status:                  "pending",
		DeviceCode:              started.DeviceCode,
		UserCode:                started.UserCode,
		VerificationURI:         verificationURI,
		VerificationURIComplete: verificationComplete,
		ExpiresAt:               now.Add(time.Duration(started.ExpiresIn * float64(time.Second))).UnixMilli(),
		IntervalMS:              interval.Milliseconds(),
		NextPollAt:              now.Add(interval).UnixMilli(),
	}
	if err := a.saveSession(session); err != nil {
		return deviceAuthStatus{}, err
	}
	return a.view(session), nil
}

func (a *deviceAuth) Status() (deviceAuthStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status()
}

func (a *deviceAuth) status() (deviceAuthStatus, error) {
	session, ok, err := a.loadSession()
	if err != nil {
		return deviceAuthStatus{}, err
	}
	if !ok {
		return deviceAuthStatus{Status: "idle"}, nil
	}
	if session.Status == "pending" && a.now().UnixMilli() >= session.ExpiresAt {
		return a.finish("expired")
	}
	return a.view(session), nil
}

func (a *deviceAuth) Poll(ctx context.Context) (deviceAuthStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	session, ok, err := a.loadSession()
	if err != nil {
		return deviceAuthStatus{}, err
	}
	if !ok {
		return deviceAuthStatus{Status: "idle"}, nil
	}
	now := a.now().UnixMilli()
	if session.Status == "pending" && now >= session.ExpiresAt {
		return a.finish("expired")
	}
	if session.Status != "pending" || now < session.NextPollAt {
		return a.view(session), nil
	}

	session.NextPollAt = now + session.IntervalMS
	if err := a.saveSession(session); err != nil {
		return deviceAuthStatus{}, err
	}
	status, body, err := a.postForm(ctx, tokenURL, url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {clientID},
		"device_code": {session.DeviceCode},
	})
	if err != nil {
		return deviceAuthStatus{}, err
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		tokens, err := tokensFromJSON(body, "", a.now())
		if err != nil {
			return a.finish("failed")
		}
		if err := a.store.CompleteDeviceAuth(*tokens); err != nil {
			return deviceAuthStatus{}, err
		}
		return deviceAuthStatus{Status: "authenticated"}, nil
	}

	code, interval := deviceAuthFailure(body)
	switch code {
	case "authorization_pending":
		return a.view(session), nil
	case "slow_down":
		if interval > 0 {
			session.IntervalMS = interval.Milliseconds()
		} else {
			session.IntervalMS += (5 * time.Second).Milliseconds()
		}
		session.NextPollAt = a.now().UnixMilli() + session.IntervalMS
		if err := a.saveSession(session); err != nil {
			return deviceAuthStatus{}, err
		}
		return a.view(session), nil
	case "access_denied", "authorization_denied":
		return a.finish("denied")
	case "expired_token":
		return a.finish("expired")
	default:
		if status >= http.StatusInternalServerError {
			return a.view(session), nil
		}
		return a.finish("failed")
	}
}

func (a *deviceAuth) postForm(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, deviceAuthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, oauthResponseLimit))
	return resp.StatusCode, body, err
}

func (a *deviceAuth) loadSession() (deviceAuthSession, bool, error) {
	encoded, err := a.store.LoadDeviceAuthSession()
	if err != nil {
		return deviceAuthSession{}, false, err
	}
	if len(encoded) == 0 {
		return deviceAuthSession{}, false, nil
	}
	var session deviceAuthSession
	if err := json.Unmarshal(encoded, &session); err != nil {
		return deviceAuthSession{}, false, fmt.Errorf("invalid stored device auth session: %w", err)
	}
	return session, true, nil
}

func (a *deviceAuth) saveSession(session deviceAuthSession) error {
	encoded, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return a.store.SaveDeviceAuthSession(encoded)
}

func (a *deviceAuth) finish(status string) (deviceAuthStatus, error) {
	if err := a.saveSession(deviceAuthSession{Status: status}); err != nil {
		return deviceAuthStatus{}, err
	}
	return deviceAuthStatus{Status: status}, nil
}

func (a *deviceAuth) view(session deviceAuthSession) deviceAuthStatus {
	if session.Status != "pending" {
		return deviceAuthStatus{Status: session.Status}
	}
	retry := int64(math.Ceil(float64(session.NextPollAt-a.now().UnixMilli()) / 1000))
	if retry < 1 {
		retry = 1
	}
	verificationURL := session.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = session.VerificationURI
	}
	return deviceAuthStatus{
		Status:            "pending",
		VerificationURL:   verificationURL,
		UserCode:          session.UserCode,
		ExpiresAt:         time.UnixMilli(session.ExpiresAt).UTC().Format(time.RFC3339Nano),
		RetryAfterSeconds: retry,
	}
}

func trustedVerificationURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("Untrusted verification URI in xAI OAuth response")
	}
	return parsed.String(), nil
}

func deviceAuthFailure(body []byte) (string, time.Duration) {
	var response struct {
		Error    string  `json:"error"`
		Interval float64 `json:"interval"`
	}
	if json.Unmarshal(body, &response) != nil {
		return "", 0
	}
	if response.Interval <= 0 {
		return response.Error, 0
	}
	return response.Error, time.Duration(response.Interval * float64(time.Second))
}

func tokensFromJSON(body []byte, previousRefreshToken string, now time.Time) (*AuthTokens, error) {
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result.AccessToken == "" {
		return nil, errors.New("invalid token response")
	}
	if result.RefreshToken == "" {
		result.RefreshToken = previousRefreshToken
	}
	if result.RefreshToken == "" {
		return nil, errors.New("token response did not include a refresh token")
	}
	if result.ExpiresIn == 0 {
		result.ExpiresIn = 3600
	}
	if result.ExpiresIn < 0 {
		return nil, errors.New("invalid token expiry")
	}
	return &AuthTokens{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(result.ExpiresIn) * time.Second).Unix(),
	}, nil
}
