// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package proxygit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strings"

	"github.com/dcoric/git-proxy-go/internal/chain"
	"github.com/dcoric/git-proxy-go/internal/giturl"
)

// fallbackHost is the upstream used for proxy paths that don't match a
// configured origin (the github.com catch-all in the Node router).
const fallbackHost = "github.com"

// maxPackBody bounds the buffered pack-POST body (Node uses a 1gb getRawBody
// limit; mirror it so a hostile client can't exhaust memory).
const maxPackBody = 1 << 30

// chainExecutor is the chain entry point the proxy depends on (satisfied by
// *chain.Engine); narrowing to an interface keeps the handler unit-testable.
type chainExecutor interface {
	Execute(ctx context.Context, r *http.Request) *chain.Action
}

// Handler is the git transport reverse proxy. It matches the inbound path
// against the configured origins, runs the chain, and forwards allowed requests
// upstream over HTTPS.
type Handler struct {
	engine    chainExecutor
	origins   []string
	transport http.RoundTripper
}

// NewHandler builds the proxy handler. origins is the set of upstream git hosts
// to recognise (the distinct hosts of the authorised repos); anything else
// falls back to github.com.
func NewHandler(engine chainExecutor, origins []string) *Handler {
	return &Handler{engine: engine, origins: origins, transport: http.DefaultTransport}
}

var packPostRegex = regexp.MustCompile(`^(?:/[^/]+)*/[^/]+\.git/(?:git-upload-pack|git-receive-pack)$`)

// isPackPost reports whether the request is a POST to a git-upload-pack or
// git-receive-pack endpoint. Port of isPackPost.
func isPackPost(r *http.Request) bool {
	return r.Method == http.MethodPost && packPostRegex.MatchString(r.URL.Path)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthcheck" {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate, proxy-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		w.Header().Set("Surrogate-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "OK")
		return
	}

	requestURI := r.RequestURI
	if requestURI == "" {
		requestURI = r.URL.RequestURI()
	}

	// Reject anything that isn't a recognisable git smart-HTTP request before
	// running the chain (mirrors proxyFilter's first guard).
	comps := giturl.ProcessURLPath(requestURI)
	if comps == nil || !giturl.ValidGitRequest(comps.GitPath, r.Header) {
		h.logAction(r, "Error", "Invalid request received")
		writePktResponse(w, http.StatusOK, "", handleMessage("Invalid request received"))
		return
	}

	// Buffer the pack body so the chain can inspect it and it can still be
	// forwarded upstream (the Go equivalent of extractRawBody).
	if err := h.bufferPackBody(r); err != nil {
		h.logAction(r, "Error", err.Error())
		sendErrorResponse(w, r, "Failed to read request body")
		return
	}

	action := h.engine.Execute(r.Context(), r)

	if action.Error || action.Blocked {
		message := "Unknown error"
		switch {
		case action.ErrorMessage != nil && *action.ErrorMessage != "":
			message = *action.ErrorMessage
		case action.BlockedMessage != nil && *action.BlockedMessage != "":
			message = *action.BlockedMessage
		}
		kind := "Blocked"
		if action.Error {
			kind = "Error"
		}
		h.logAction(r, kind, message)
		sendErrorResponse(w, r, message)
		return
	}

	h.logAction(r, "Allowed", "")
	h.forward(w, r)
}

// bufferPackBody reads the body of a pack POST into memory, stashes it on the
// request context for the chain, and resets r.Body so it is still forwarded.
func (h *Handler) bufferPackBody(r *http.Request) error {
	if !isPackPost(r) || r.Body == nil {
		return nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxPackBody))
	_ = r.Body.Close()
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.ContentLength = int64(len(buf))
	*r = *r.WithContext(chain.WithRawBody(r.Context(), buf))
	return nil
}

// forward reverse-proxies the request to the resolved upstream host over HTTPS.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request) {
	host, upstreamPath := h.resolveTarget(r.URL.Path)
	rp := &httputil.ReverseProxy{
		Director: func(out *http.Request) {
			out.URL.Scheme = "https"
			out.URL.Host = host
			out.URL.Path = upstreamPath
			out.Host = host
		},
		Transport: h.transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Error("upstream proxy error", "host", host, "err", err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// resolveTarget maps an inbound path to the upstream host and path. A path
// prefixed with a configured origin ("/github.com/…") targets that host with
// the prefix stripped; anything else falls back to github.com with the path
// unchanged. This reproduces the upstream URLs the Node router builds.
func (h *Handler) resolveTarget(path string) (host, upstreamPath string) {
	for _, o := range h.origins {
		if path == "/"+o || strings.HasPrefix(path, "/"+o+"/") {
			return o, strings.TrimPrefix(path, "/"+o)
		}
	}
	return fallbackHost, path
}

// logAction logs one processed-request line (the Go equivalent of logAction).
func (h *Handler) logAction(r *http.Request, kind, message string) {
	attrs := []any{
		"type", kind,
		"url", r.RequestURI,
		"host", r.Host,
		"user_agent", r.Header.Get("User-Agent"),
	}
	if message != "" && kind != "Allowed" {
		attrs = append(attrs, "message", message)
	}
	slog.Info("git proxy action", attrs...)
}

// handleMessage formats a sideband-2 pkt-line carrying a human-readable message
// (Node handleMessage), used for receive-pack-style error responses.
func handleMessage(message string) string {
	body := "\t" + message
	length := 6 + len(body)
	return fmt.Sprintf("%04x\x02%s\n0000", length, body)
}

// handleRefsErrorMessage formats a git "ERR" pkt-line for the ref-advertisement
// (GET /info/refs) error case (Node handleRefsErrorMessage).
func handleRefsErrorMessage(message string) string {
	body := "ERR " + message
	length := 4 + len(body)
	return fmt.Sprintf("%04x%s\n0000", length, body)
}

// sendErrorResponse answers a blocked/errored git request with the appropriate
// git protocol error packet. Port of sendErrorResponse.
func sendErrorResponse(w http.ResponseWriter, r *http.Request, message string) {
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/info/refs") {
		writePktResponse(w, http.StatusOK, "application/x-git-upload-pack-advertisement", handleRefsErrorMessage(message))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/x-git-receive-pack-result")
	h.Set("Expires", "Fri, 01 Jan 1980 00:00:00 GMT")
	h.Set("Pragma", "no-cache")
	h.Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	h.Set("Vary", "Accept-Encoding")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Connection", "close")
	writePktResponse(w, http.StatusOK, "", handleMessage(message))
}

func writePktResponse(w http.ResponseWriter, status int, contentType, body string) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
