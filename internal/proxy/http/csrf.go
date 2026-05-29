// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package proxyhttp

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
)

// CSRF cookie/header names. These must match what the React UI already speaks:
// it reads the `csrf` cookie and echoes it in the X-CSRF-TOKEN header
// (ui/services/auth.ts:66). The names are fixed by that contract.
const (
	CSRFCookieName = "csrf"
	CSRFHeaderName = "X-CSRF-TOKEN"
)

// CSRFProtection returns middleware implementing the stateless double-submit
// pattern the existing UI is built around: safe requests mint a readable `csrf`
// cookie; unsafe requests (POST/PUT/PATCH/DELETE) must echo that cookie's value
// in the X-CSRF-TOKEN header or are rejected with 403.
//
// P0-6 status: this is the interop prototype that proves the UI's contract.
// The Node build uses lusca, whose token is session-backed; P3-2 reconciles
// this middleware with sessions and gates it on the csrfProtection config flag.
// Until then it is intentionally NOT wired into NewRouter.
func CSRFProtection() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			if c, err := r.Cookie(CSRFCookieName); err == nil {
				token = c.Value
			}

			if isSafeMethod(r.Method) {
				if token == "" {
					http.SetCookie(w, newCSRFCookie(newCSRFToken()))
				}
				next.ServeHTTP(w, r)
				return
			}

			header := r.Header.Get(CSRFHeaderName)
			if token == "" || header == "" ||
				subtle.ConstantTimeCompare([]byte(token), []byte(header)) != 1 {
				http.Error(w, "invalid CSRF token", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isSafeMethod reports whether the HTTP method is non-mutating and therefore
// exempt from CSRF enforcement (the set lusca/OWASP treat as safe).
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// newCSRFToken returns a 256-bit random token, hex-encoded.
func newCSRFToken() string {
	b := make([]byte, 32)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newCSRFCookie builds the readable double-submit cookie. It is deliberately
// not HttpOnly so the SPA can read it via document.cookie and echo it back.
// Secure is left off here so the prototype works over plain HTTP in tests; P3-2
// will set Secure to track the session cookie's flags.
func newCSRFCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	}
}
