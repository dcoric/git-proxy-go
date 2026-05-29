// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package proxyhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// okHandler is the protected handler under test.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// csrfCookieFrom extracts the issued csrf cookie from a response, or nil.
func csrfCookieFrom(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == CSRFCookieName {
			return c
		}
	}
	return nil
}

// TestCSRFIssuesReadableCookieOnSafeRequest: a GET must mint a `csrf` cookie the
// SPA can read (not HttpOnly), so it can echo it back on later mutations.
func TestCSRFIssuesReadableCookieOnSafeRequest(t *testing.T) {
	h := CSRFProtection()(okHandler())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/healthcheck", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rr.Code)
	}
	cookie := csrfCookieFrom(rr.Result())
	if cookie == nil {
		t.Fatal("no csrf cookie set on safe request")
	}
	if cookie.Value == "" {
		t.Error("csrf cookie value is empty")
	}
	if cookie.HttpOnly {
		t.Error("csrf cookie is HttpOnly; the UI must be able to read it via document.cookie")
	}
}

// TestCSRFAcceptsMatchingDoubleSubmit: a mutation that echoes the cookie value
// in X-CSRF-TOKEN is accepted.
func TestCSRFAcceptsMatchingDoubleSubmit(t *testing.T) {
	h := CSRFProtection()(okHandler())

	// First, a safe request to obtain the cookie.
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/", nil))
	cookie := csrfCookieFrom(get.Result())
	if cookie == nil {
		t.Fatal("setup: no csrf cookie issued")
	}

	post := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", nil)
	req.AddCookie(cookie)
	req.Header.Set(CSRFHeaderName, cookie.Value)
	h.ServeHTTP(post, req)

	if post.Code != http.StatusOK {
		t.Errorf("matching double-submit POST status = %d, want 200", post.Code)
	}
}

func TestCSRFRejectsMissingHeader(t *testing.T) {
	h := CSRFProtection()(okHandler())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "abc123"})
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("POST with cookie but no header status = %d, want 403", rr.Code)
	}
}

func TestCSRFRejectsMismatch(t *testing.T) {
	h := CSRFProtection()(okHandler())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "abc123"})
	req.Header.Set(CSRFHeaderName, "different-value")
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("POST with mismatched header status = %d, want 403", rr.Code)
	}
}

func TestCSRFRejectsNoCookie(t *testing.T) {
	h := CSRFProtection()(okHandler())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push", nil)
	req.Header.Set(CSRFHeaderName, "orphan-token")
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("POST with no cookie status = %d, want 403", rr.Code)
	}
}
