// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package cli is the git-proxy-cli implementation (D-2): a thin client over the
// management API for listing and acting on held pushes from the terminal. It is
// the Go port of @finos/git-proxy-cli.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
)

// CSRF cookie/header names, matching the proxy's double-submit middleware.
const (
	csrfCookieName = "csrf"
	csrfHeaderName = "X-CSRF-TOKEN"
)

// Client talks to a git-proxy management server, persisting its session and CSRF
// cookies to a file across invocations (the CLI is stateless between runs).
type Client struct {
	baseURL    string
	cookieFile string
	http       *http.Client
}

// New builds a client for baseURL, loading any persisted cookies from
// cookieFile.
func New(baseURL, cookieFile string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		cookieFile: cookieFile,
		http:       &http.Client{Jar: jar},
	}
	c.loadCookies()
	return c, nil
}

// BaseURL is the configured management server URL.
func (c *Client) BaseURL() string { return c.baseURL }

type storedCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (c *Client) base() *url.URL {
	u, _ := url.Parse(c.baseURL)
	return u
}

// loadCookies seeds the jar from the cookie file (best-effort: a missing or
// unreadable file just means "not logged in").
func (c *Client) loadCookies() {
	data, err := os.ReadFile(c.cookieFile)
	if err != nil {
		return
	}
	var stored []storedCookie
	if json.Unmarshal(data, &stored) != nil {
		return
	}
	cookies := make([]*http.Cookie, 0, len(stored))
	for _, s := range stored {
		cookies = append(cookies, &http.Cookie{Name: s.Name, Value: s.Value})
	}
	c.http.Jar.SetCookies(c.base(), cookies)
}

// saveCookies persists the jar's cookies for the base host to the cookie file
// (0600 — it holds the session token).
func (c *Client) saveCookies() error {
	cookies := c.http.Jar.Cookies(c.base())
	stored := make([]storedCookie, 0, len(cookies))
	for _, ck := range cookies {
		stored = append(stored, storedCookie{Name: ck.Name, Value: ck.Value})
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	return os.WriteFile(c.cookieFile, data, 0o600)
}

// clearCookies removes the persisted cookie file (logout).
func (c *Client) clearCookies() error {
	if err := os.Remove(c.cookieFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (c *Client) cookieValue(name string) string {
	for _, ck := range c.http.Jar.Cookies(c.base()) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

// ensureCSRF returns the double-submit token, minting one with a safe request if
// the jar has none yet (the proxy sets the readable `csrf` cookie on any GET).
func (c *Client) ensureCSRF() (string, error) {
	if v := c.cookieValue(csrfCookieName); v != "" {
		return v, nil
	}
	if err := c.do(http.MethodGet, "/api/v1/healthcheck", nil, nil); err != nil {
		return "", err
	}
	return c.cookieValue(csrfCookieName), nil
}

// do issues a request, echoing the CSRF token on unsafe methods, persisting
// cookies afterwards, and decoding a JSON response into out when provided.
func (c *Client) do(method, path string, reqBody, out any) error {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !isSafeMethod(method) {
		token, err := c.ensureCSRF()
		if err != nil {
			return err
		}
		if token != "" {
			req.Header.Set(csrfHeaderName, token)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	_ = c.saveCookies()

	if resp.StatusCode >= http.StatusBadRequest {
		return httpError(resp.StatusCode, raw)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// httpError turns a non-2xx response into an error, preferring the API's
// {"message": …} body when present.
func httpError(status int, body []byte) error {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &m) == nil && m.Message != "" {
		return fmt.Errorf("server returned %d: %s", status, m.Message)
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return fmt.Errorf("server returned %d", status)
	}
	return fmt.Errorf("server returned %d: %s", status, msg)
}
