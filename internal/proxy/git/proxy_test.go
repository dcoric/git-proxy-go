// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package proxygit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dcoric/git-proxy-go/internal/chain"
)

// fakeEngine is a stand-in chain executor returning a fixed action.
type fakeEngine struct {
	action *chain.Action
	called bool
}

func (f *fakeEngine) Execute(_ context.Context, _ *http.Request) *chain.Action {
	f.called = true
	if f.action != nil {
		return f.action
	}
	return &chain.Action{}
}

// capturingRT records the upstream URL the reverse proxy dialled and returns a
// canned response (so tests need no real upstream).
type capturingRT struct{ gotURL string }

func (c *capturingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.gotURL = r.URL.String()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("UPSTREAM-OK")),
		Header:     make(http.Header),
	}, nil
}

func refsRequest(agent string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/github.com/finos/git-proxy.git/info/refs?service=git-upload-pack", nil)
	if agent != "" {
		r.Header.Set("User-Agent", agent)
	}
	return r
}

func newHandler(t *testing.T, action *chain.Action, origins []string) (*Handler, *fakeEngine, *capturingRT) {
	t.Helper()
	fe := &fakeEngine{action: action}
	rt := &capturingRT{}
	h := NewHandler(fe, origins)
	h.transport = rt
	return h, fe, rt
}

func TestHealthcheck(t *testing.T) {
	h, fe, _ := newHandler(t, nil, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthcheck", nil))

	if rr.Code != http.StatusOK || rr.Body.String() != "OK" {
		t.Errorf("healthcheck = %d %q, want 200 OK", rr.Code, rr.Body.String())
	}
	if fe.called {
		t.Error("chain must not run for healthcheck")
	}
}

func TestInvalidGitRequestNotProxied(t *testing.T) {
	h, fe, rt := newHandler(t, nil, []string{"github.com"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, refsRequest("curl/8.0")) // non-git agent => invalid

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if rr.Body.String() != handleMessage("Invalid request received") {
		t.Errorf("body = %q, want invalid-request pkt-line", rr.Body.String())
	}
	if fe.called {
		t.Error("chain must not run for an invalid git request")
	}
	if rt.gotURL != "" {
		t.Errorf("request was forwarded to %q, want none", rt.gotURL)
	}
}

func TestAllowedRequestForwarded(t *testing.T) {
	h, fe, rt := newHandler(t, &chain.Action{}, []string{"github.com"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, refsRequest("git/2.40"))

	if !fe.called {
		t.Error("chain should run for a valid request")
	}
	const want = "https://github.com/finos/git-proxy.git/info/refs?service=git-upload-pack"
	if rt.gotURL != want {
		t.Errorf("forwarded to %q, want %q", rt.gotURL, want)
	}
	if rr.Body.String() != "UPSTREAM-OK" {
		t.Errorf("body = %q, want upstream response", rr.Body.String())
	}
}

func TestLegacyPathFallsBackToGitHub(t *testing.T) {
	// A host-less legacy path doesn't match the configured origin, so it falls
	// back to github.com with the path unchanged.
	h, _, rt := newHandler(t, &chain.Action{}, []string{"github.com"})
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/finos/git-proxy.git/info/refs?service=git-upload-pack", nil)
	r.Header.Set("User-Agent", "git/2.40")
	h.ServeHTTP(rr, r)

	const want = "https://github.com/finos/git-proxy.git/info/refs?service=git-upload-pack"
	if rt.gotURL != want {
		t.Errorf("forwarded to %q, want %q", rt.gotURL, want)
	}
}

func TestConfiguredOriginStripsPrefix(t *testing.T) {
	h, _, rt := newHandler(t, &chain.Action{}, []string{"gitlab.com"})
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/gitlab.com/foo/bar.git/git-upload-pack", nil)
	r.Header.Set("User-Agent", "git/2.40")
	r.Header.Set("Accept", "application/x-git-upload-pack-result")
	h.ServeHTTP(rr, r)

	const want = "https://gitlab.com/foo/bar.git/git-upload-pack"
	if rt.gotURL != want {
		t.Errorf("forwarded to %q, want %q", rt.gotURL, want)
	}
}

func TestBlockedRefsRequestReturnsErrorPacket(t *testing.T) {
	msg := "repo not authorised"
	action := &chain.Action{}
	action.Blocked = true
	action.BlockedMessage = &msg

	h, _, rt := newHandler(t, action, []string{"github.com"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, refsRequest("git/2.40"))

	if ct := rr.Header().Get("Content-Type"); ct != "application/x-git-upload-pack-advertisement" {
		t.Errorf("content-type = %q, want upload-pack-advertisement", ct)
	}
	if rr.Body.String() != handleRefsErrorMessage(msg) {
		t.Errorf("body = %q, want refs error pkt-line", rr.Body.String())
	}
	if rt.gotURL != "" {
		t.Errorf("blocked request was forwarded to %q, want none", rt.gotURL)
	}
}

func TestErroredReceivePackReturnsErrorPacket(t *testing.T) {
	msg := "boom"
	action := &chain.Action{}
	action.Error = true
	action.ErrorMessage = &msg

	h, _, rt := newHandler(t, action, []string{"github.com"})
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/github.com/finos/git-proxy.git/git-receive-pack", nil)
	r.Header.Set("User-Agent", "git/2.40")
	r.Header.Set("Accept", "application/x-git-receive-pack-result")
	r.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	h.ServeHTTP(rr, r)

	if ct := rr.Header().Get("Content-Type"); ct != "application/x-git-receive-pack-result" {
		t.Errorf("content-type = %q, want receive-pack-result", ct)
	}
	if rr.Body.String() != handleMessage(msg) {
		t.Errorf("body = %q, want receive-pack error pkt-line", rr.Body.String())
	}
	if rt.gotURL != "" {
		t.Errorf("errored request was forwarded to %q, want none", rt.gotURL)
	}
}

func TestPackBodyBufferedForChain(t *testing.T) {
	// A pack POST body must be buffered onto the context (for parsePush) and
	// still be forwardable.
	var seen []byte
	fe := &fakeEngineFunc{fn: func(ctx context.Context, _ *http.Request) *chain.Action {
		if b, ok := chain.RawBody(ctx); ok {
			seen = b
		}
		return &chain.Action{}
	}}
	rt := &capturingRT{}
	h := NewHandler(fe, []string{"github.com"})
	h.transport = rt

	body := "PACKDATA"
	r := httptest.NewRequest(http.MethodPost, "/github.com/finos/git-proxy.git/git-receive-pack", strings.NewReader(body))
	r.Header.Set("User-Agent", "git/2.40")
	r.Header.Set("Accept", "application/x-git-receive-pack-result")
	r.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	h.ServeHTTP(httptest.NewRecorder(), r)

	if string(seen) != body {
		t.Errorf("chain saw body %q, want %q", seen, body)
	}
}

// fakeEngineFunc lets a test inspect the request/context inside Execute.
type fakeEngineFunc struct {
	fn func(ctx context.Context, r *http.Request) *chain.Action
}

func (f *fakeEngineFunc) Execute(ctx context.Context, r *http.Request) *chain.Action {
	return f.fn(ctx, r)
}
