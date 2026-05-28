// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package proxyhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	srv := httptest.NewServer(NewRouter(Options{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/healthcheck")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if got, want := string(buf[:n]), `{"message":"ok"}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestSecurityHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter(Options{}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/healthcheck", nil))

	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "SAMEORIGIN",
		"Referrer-Policy":        "same-origin",
	}
	for h, want := range checks {
		if got := rr.Header().Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
}

func TestCORSPreflightReflectsOriginWithCredentials(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/healthcheck", nil)
	req.Header.Set("Origin", "https://ui.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	// Browsers send Access-Control-Request-Headers lowercase (Fetch spec), which
	// is what rs/cors matches against; CSRF preflight uses x-csrf-token.
	req.Header.Set("Access-Control-Request-Headers", "x-csrf-token")
	NewRouter(Options{}).ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://ui.example.com" {
		t.Errorf("Allow-Origin = %q, want reflected origin", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q, want true", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); got != "x-csrf-token" {
		t.Errorf("Allow-Headers = %q, want x-csrf-token", got)
	}
}

func TestRateLimitReturns429(t *testing.T) {
	// burst of 1: the second immediate request must be limited.
	h := NewRouter(Options{RateLimitRPS: 0.0001, RateLimitBurst: 1})

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/healthcheck", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", first.Code)
	}

	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/v1/healthcheck", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Errorf("second request status = %d, want 429", second.Code)
	}
}
