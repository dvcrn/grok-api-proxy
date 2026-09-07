package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type memoryTokenStore struct {
	tokens  *AuthTokens
	session []byte
}

func (s *memoryTokenStore) LoadTokens() (*AuthTokens, error) {
	if s.tokens == nil {
		return nil, errNotAuthenticated
	}
	copy := *s.tokens
	return &copy, nil
}

func (s *memoryTokenStore) SaveTokens(tokens AuthTokens) error {
	copy := tokens
	s.tokens = &copy
	return nil
}

func (s *memoryTokenStore) LoadDeviceAuthSession() ([]byte, error) {
	return append([]byte(nil), s.session...), nil
}

func (s *memoryTokenStore) SaveDeviceAuthSession(session []byte) error {
	s.session = append([]byte(nil), session...)
	return nil
}

func (s *memoryTokenStore) CompleteDeviceAuth(tokens AuthTokens) error {
	if err := s.SaveTokens(tokens); err != nil {
		return err
	}
	return s.SaveDeviceAuthSession([]byte(`{"status":"authenticated"}`))
}

type testHTTPClientFunc func(*http.Request) (*http.Response, error)

func (f testHTTPClientFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDeviceAuthStartAndPoll(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := &memoryTokenStore{}
	requests := 0
	client := testHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if requests == 1 {
			if req.URL.String() != deviceCodeURL || form.Get("client_id") != clientID || form.Get("scope") != scope || form.Get("referrer") != "pi" {
				t.Errorf("device request = %s %v", req.URL, form)
			}
			return jsonResponse(http.StatusOK, `{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://auth.x.ai/activate","verification_uri_complete":"https://auth.x.ai/activate?user_code=ABCD-EFGH","expires_in":900,"interval":5}`), nil
		}
		if req.URL.String() != tokenURL || form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || form.Get("device_code") != "device" {
			t.Errorf("poll request = %s %v", req.URL, form)
		}
		return jsonResponse(http.StatusOK, `{"access_token":"access","refresh_token":"refresh","expires_in":3600}`), nil
	})
	auth := newDeviceAuth(store, client)
	auth.now = func() time.Time { return now }

	started, err := auth.Start(t.Context())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if started.Status != "pending" || started.UserCode != "ABCD-EFGH" || !strings.HasPrefix(started.VerificationURL, "https://auth.x.ai/") {
		t.Errorf("Start() = %+v", started)
	}

	now = now.Add(5 * time.Second)
	completed, err := auth.Poll(t.Context())
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if completed.Status != "authenticated" {
		t.Errorf("Poll() = %+v", completed)
	}
	if store.tokens == nil || store.tokens.AccessToken != "access" || store.tokens.RefreshToken != "refresh" || store.tokens.ExpiresAt != now.Add(time.Hour).Unix() {
		t.Errorf("stored tokens = %+v", store.tokens)
	}
}

func TestDeviceAuthPollWaitsForInterval(t *testing.T) {
	now := time.Now()
	store := &memoryTokenStore{
		session: []byte(`{"status":"pending","deviceCode":"device","userCode":"code","verificationUri":"https://auth.x.ai/activate","expiresAt":` + number(now.Add(time.Minute).UnixMilli()) + `,"intervalMs":5000,"nextPollAt":` + number(now.Add(5*time.Second).UnixMilli()) + `}`),
	}
	auth := newDeviceAuth(store, testHTTPClientFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("HTTP client called before poll interval")
		return nil, nil
	}))
	auth.now = func() time.Time { return now }

	status, err := auth.Poll(t.Context())
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if status.Status != "pending" || status.RetryAfterSeconds != 5 {
		t.Errorf("Poll() = %+v", status)
	}
}

func TestTrustedVerificationURL(t *testing.T) {
	if _, err := trustedVerificationURL("http://auth.x.ai/activate"); err == nil {
		t.Fatal("trustedVerificationURL accepted HTTP")
	}
	if got, err := trustedVerificationURL("https://auth.x.ai/activate"); err != nil || got == "" {
		t.Fatalf("trustedVerificationURL() = %q, %v", got, err)
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func number(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
