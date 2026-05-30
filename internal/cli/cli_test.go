// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProxy mimics the management server's cookie + CSRF behaviour: GETs mint a
// readable `csrf` cookie, unsafe methods require X-CSRF-TOKEN to match it, and
// login issues a `session` cookie. It records the last request for assertions.
type fakeProxy struct {
	server      *httptest.Server
	lastQuery   string
	lastBody    map[string]any
	attestation []map[string]any
}

func newFakeProxy(t *testing.T) *fakeProxy {
	t.Helper()
	f := &fakeProxy{}
	mux := http.NewServeMux()

	setCSRF := func(w http.ResponseWriter) {
		http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: "tok-123", Path: "/"})
	}
	csrfOK := func(r *http.Request) bool {
		c, err := r.Cookie(csrfCookieName)
		return err == nil && c.Value != "" && r.Header.Get(csrfHeaderName) == c.Value
	}
	requireCSRF := func(w http.ResponseWriter, r *http.Request) bool {
		if !csrfOK(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "invalid csrf token"})
			return false
		}
		return true
	}
	capture := func(r *http.Request) {
		f.lastQuery = r.URL.RawQuery
		f.lastBody = nil
		var b map[string]any
		if json.NewDecoder(r.Body).Decode(&b) == nil {
			f.lastBody = b
		}
	}

	mux.HandleFunc("GET /api/v1/healthcheck", func(w http.ResponseWriter, _ *http.Request) {
		setCSRF(w)
		writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
	})
	mux.HandleFunc("GET /api/auth/config", func(w http.ResponseWriter, _ *http.Request) {
		setCSRF(w)
		writeJSON(w, http.StatusOK, map[string]any{"usernamePasswordMethod": true})
	})
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !requireCSRF(w, r) {
			return
		}
		capture(r)
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "sess-abc", Path: "/"})
		setCSRF(w)
		writeJSON(w, http.StatusOK, map[string]string{"message": "logged in"})
	})
	mux.HandleFunc("GET /api/auth/profile", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err != nil || c.Value == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "not logged in"})
			return
		}
		setCSRF(w)
		writeJSON(w, http.StatusOK, map[string]string{"username": "alice"})
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if !requireCSRF(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "logged out"})
	})
	mux.HandleFunc("GET /api/v1/push/", func(w http.ResponseWriter, r *http.Request) {
		setCSRF(w)
		capture(r)
		writeJSON(w, http.StatusOK, []map[string]any{
			{"id": "p1", "repo": "github.com/o/r.git", "blocked": true},
		})
	})
	mux.HandleFunc("GET /api/v1/config/attestation", func(w http.ResponseWriter, _ *http.Request) {
		setCSRF(w)
		writeJSON(w, http.StatusOK, map[string]any{"questions": f.attestation})
	})
	mux.HandleFunc("POST /api/v1/push/{id}/authorise", func(w http.ResponseWriter, r *http.Request) {
		if !requireCSRF(w, r) {
			return
		}
		capture(r)
		writeJSON(w, http.StatusOK, map[string]string{"message": "authorised"})
	})
	mux.HandleFunc("POST /api/v1/push/{id}/reject", func(w http.ResponseWriter, r *http.Request) {
		if !requireCSRF(w, r) {
			return
		}
		capture(r)
		writeJSON(w, http.StatusOK, map[string]string{"message": "rejected"})
	})
	mux.HandleFunc("POST /api/v1/push/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !requireCSRF(w, r) {
			return
		}
		capture(r)
		writeJSON(w, http.StatusOK, map[string]string{"message": "canceled"})
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newTestClient(t *testing.T, f *fakeProxy) *Client {
	t.Helper()
	c, err := New(f.server.URL, filepath.Join(t.TempDir(), "cookies.json"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestLoginPersistsSessionAcrossClients(t *testing.T) {
	f := newFakeProxy(t)
	cookieFile := filepath.Join(t.TempDir(), "cookies.json")

	c1, err := New(f.server.URL, cookieFile)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c1.Login("alice", "pw"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if f.lastBody["username"] != "alice" || f.lastBody["password"] != "pw" {
		t.Errorf("login body = %v", f.lastBody)
	}

	// A fresh client loading the same cookie file must be authenticated.
	c2, err := New(f.server.URL, cookieFile)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c2.cookieValue("session") != "sess-abc" {
		t.Errorf("session cookie not persisted/reloaded, got %q", c2.cookieValue("session"))
	}
	if _, err := c2.List(Filters{}); err != nil {
		t.Errorf("List with reloaded session: %v", err)
	}
}

func TestUnsafeRequestsSendCSRFToken(t *testing.T) {
	f := newFakeProxy(t)
	c := newTestClient(t, f)
	// No prior request: Cancel must mint a CSRF token (GET) then echo it on the
	// POST, or the fake rejects with 403.
	if err := c.Cancel("p1"); err != nil {
		t.Fatalf("Cancel (CSRF double-submit) failed: %v", err)
	}
}

func TestListAppliesFilters(t *testing.T) {
	f := newFakeProxy(t)
	c := newTestClient(t, f)
	yes := true
	if _, err := c.List(Filters{Blocked: &yes}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(f.lastQuery, "blocked=true") {
		t.Errorf("filter not sent as query param: %q", f.lastQuery)
	}
}

func TestListNoFiltersSendsNoQuery(t *testing.T) {
	f := newFakeProxy(t)
	c := newTestClient(t, f)
	if _, err := c.List(Filters{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if f.lastQuery != "" {
		t.Errorf("expected no query params, got %q", f.lastQuery)
	}
}

func TestAuthoriseAnswersConfiguredAttestation(t *testing.T) {
	f := newFakeProxy(t)
	f.attestation = []map[string]any{{"label": "Reviewed?"}, {"label": "Tested?"}}
	c := newTestClient(t, f)

	if err := c.Authorise("p1"); err != nil {
		t.Fatalf("Authorise: %v", err)
	}
	params, _ := f.lastBody["params"].(map[string]any)
	answers, _ := params["attestation"].([]any)
	if len(answers) != 2 {
		t.Fatalf("expected 2 attestation answers, got %v", f.lastBody)
	}
	for _, a := range answers {
		m := a.(map[string]any)
		if m["checked"] != true || m["label"] == "" {
			t.Errorf("attestation answer not checked/labelled: %v", m)
		}
	}
}

func TestRejectSendsReason(t *testing.T) {
	f := newFakeProxy(t)
	c := newTestClient(t, f)
	if err := c.Reject("p1", "nope"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if f.lastBody["reason"] != "nope" {
		t.Errorf("reject reason = %v", f.lastBody["reason"])
	}
}

func TestLogoutClearsCookieFile(t *testing.T) {
	f := newFakeProxy(t)
	cookieFile := filepath.Join(t.TempDir(), "cookies.json")
	c, _ := New(f.server.URL, cookieFile)
	if err := c.Login("alice", "pw"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := c.Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := os.Stat(cookieFile); !os.IsNotExist(err) {
		t.Errorf("cookie file not removed on logout (stat err=%v)", err)
	}
}

func TestServerErrorSurfacesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Cannot approve your own changes"})
	}))
	defer srv.Close()
	c, _ := New(srv.URL, filepath.Join(t.TempDir(), "c.json"))
	err := c.Reject("p1", "x")
	if err == nil || !strings.Contains(err.Error(), "Cannot approve your own changes") {
		t.Errorf("expected server message in error, got %v", err)
	}
}
