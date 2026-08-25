package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler is a sentinel downstream handler that records whether it ran.
func okHandler(called *bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	}
}

// Unlike the ADMIN_KEY middleware this replaced, an unset key fails closed
// rather than disabling authentication: a missing key is a misconfiguration,
// not an invitation to serve the proxy unauthenticated.
func TestAdminMiddleware_FailsClosedWhenUnset(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", "")

	var called bool
	h := adminMiddleware(okHandler(&called))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if called {
		t.Fatal("handler must not run when ADMIN_API_KEY is unset")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestAdminMiddleware_Accepts(t *testing.T) {
	cases := []struct {
		name   string
		apply  func(*http.Request)
		target string
	}{
		{
			name:  "bearer token",
			apply: func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") },
		},
		{
			name:  "bearer scheme is case insensitive",
			apply: func(r *http.Request) { r.Header.Set("Authorization", "bearer s3cret") },
		},
		{
			name:  "x-api-key header",
			apply: func(r *http.Request) { r.Header.Set("X-API-Key", "s3cret") },
		},
		{
			name:   "key query parameter",
			apply:  func(r *http.Request) {},
			target: "/?key=s3cret",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ADMIN_API_KEY", "s3cret")

			target := tc.target
			if target == "" {
				target = "/"
			}

			var called bool
			h := adminMiddleware(okHandler(&called))

			req := httptest.NewRequest(http.MethodGet, target, nil)
			tc.apply(req)
			rec := httptest.NewRecorder()
			h(rec, req)

			if !called {
				t.Fatal("handler should run for an authorized request")
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
		})
	}
}

func TestAdminMiddleware_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		apply  func(*http.Request)
		target string
	}{
		{"missing credentials", func(r *http.Request) {}, ""},
		{"wrong token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, ""},
		{"missing bearer prefix", func(r *http.Request) { r.Header.Set("Authorization", "s3cret") }, ""},
		{"empty bearer token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") }, ""},
		{"wrong x-api-key", func(r *http.Request) { r.Header.Set("X-API-Key", "nope") }, ""},
		{"wrong key parameter", func(r *http.Request) {}, "/?key=nope"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ADMIN_API_KEY", "s3cret")

			target := tc.target
			if target == "" {
				target = "/"
			}

			var called bool
			h := adminMiddleware(okHandler(&called))

			req := httptest.NewRequest(http.MethodGet, target, nil)
			tc.apply(req)
			rec := httptest.NewRecorder()
			h(rec, req)

			if called {
				t.Fatal("handler should not run for an unauthorized request")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}
