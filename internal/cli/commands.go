// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

// PushSummary is the subset of a push the CLI lists.
type PushSummary struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	Repo       string `json:"repo"`
	Error      bool   `json:"error"`
	Blocked    bool   `json:"blocked"`
	AllowPush  bool   `json:"allowPush"`
	Authorised bool   `json:"authorised"`
	Canceled   bool   `json:"canceled"`
	Rejected   bool   `json:"rejected"`
}

// Login authenticates by username/password and persists the session cookie.
func (c *Client) Login(username, password string) error {
	// A safe request first so the jar holds a CSRF token for the login POST.
	if _, err := c.ensureCSRF(); err != nil {
		return err
	}
	if err := c.do(http.MethodPost, "/api/auth/login",
		map[string]string{"username": username, "password": password}, nil); err != nil {
		return err
	}
	// Confirm the session is usable.
	if err := c.do(http.MethodGet, "/api/auth/profile", nil, nil); err != nil {
		return fmt.Errorf("login succeeded but profile check failed: %w", err)
	}
	return nil
}

// Logout ends the session and clears the persisted cookies.
func (c *Client) Logout() error {
	err := c.do(http.MethodPost, "/api/auth/logout", nil, nil)
	// Drop local credentials regardless of the server's response.
	if cerr := c.clearCookies(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// Filters are the push flags `ls` can filter on; a nil field means "don't
// filter on this flag".
type Filters struct {
	Error, Blocked, AllowPush, Authorised, Canceled, Rejected *bool
}

// List fetches pushes, applying any set boolean filters as query params.
func (c *Client) List(f Filters) ([]PushSummary, error) {
	q := url.Values{}
	add := func(key string, v *bool) {
		if v != nil {
			q.Set(key, strconv.FormatBool(*v))
		}
	}
	add("error", f.Error)
	add("blocked", f.Blocked)
	add("allowPush", f.AllowPush)
	add("authorised", f.Authorised)
	add("canceled", f.Canceled)
	add("rejected", f.Rejected)

	path := "/api/v1/push/"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var pushes []PushSummary
	if err := c.do(http.MethodGet, path, nil, &pushes); err != nil {
		return nil, err
	}
	return pushes, nil
}

// Authorise approves a held push, auto-answering the configured attestation
// questions (the CLI cannot prompt interactively).
func (c *Client) Authorise(id string) error {
	answers, err := c.attestationAnswers()
	if err != nil {
		return err
	}
	body := map[string]any{"params": map[string]any{"attestation": answers}}
	return c.do(http.MethodPost, "/api/v1/push/"+id+"/authorise", body, nil)
}

// Reject declines a held push with a reason.
func (c *Client) Reject(id, reason string) error {
	return c.do(http.MethodPost, "/api/v1/push/"+id+"/reject",
		map[string]string{"reason": reason}, nil)
}

// Cancel withdraws a held push.
func (c *Client) Cancel(id string) error {
	return c.do(http.MethodPost, "/api/v1/push/"+id+"/cancel", nil, nil)
}

// attestationAnswer mirrors db.AttestationAnswer for the authorise body.
type attestationAnswer struct {
	Label   string `json:"label"`
	Checked bool   `json:"checked"`
}

// attestationAnswers reads the configured attestation questions and returns a
// checked answer for each, so authorise satisfies the server's validation
// whatever the deployment configures.
func (c *Client) attestationAnswers() ([]attestationAnswer, error) {
	var cfg struct {
		Questions []struct {
			Label string `json:"label"`
		} `json:"questions"`
	}
	if err := c.do(http.MethodGet, "/api/v1/config/attestation", nil, &cfg); err != nil {
		return nil, err
	}
	answers := make([]attestationAnswer, 0, len(cfg.Questions))
	for _, q := range cfg.Questions {
		answers = append(answers, attestationAnswer{Label: q.Label, Checked: true})
	}
	return answers, nil
}

// PrintPushes writes a push list as an aligned table.
func PrintPushes(w io.Writer, pushes []PushSummary) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tREPO\tSTATE")
	for _, p := range pushes {
		repo := p.Repo
		if repo == "" {
			repo = p.URL
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", p.ID, repo, pushState(p))
	}
	_ = tw.Flush()
}

// pushState summarises a push's flags into a human label.
func pushState(p PushSummary) string {
	var states []string
	flags := map[string]bool{
		"error":      p.Error,
		"blocked":    p.Blocked,
		"approved":   p.AllowPush,
		"authorised": p.Authorised,
		"canceled":   p.Canceled,
		"rejected":   p.Rejected,
	}
	for name, set := range flags {
		if set {
			states = append(states, name)
		}
	}
	if len(states) == 0 {
		return "pending"
	}
	sort.Strings(states)
	return strings.Join(states, ",")
}
