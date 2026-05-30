// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package parity is the traffic-mirroring harness (P6-1): it sends the same
// request to the Node and Go proxies and reports divergences in their responses.
// It is the foundation for the JSON byte-diff (P6-2) and git semantic-diff
// (P6-3) gates and the soak (P6-5).
//
// The differs are unit-tested in-process; the live comparison against running
// Node + Go backends is driven by TestParityAgainstBackends (env-guarded), so it
// runs in staging where both proxies exist.
package parity

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Kind selects how a scenario's two responses are compared.
type Kind int

const (
	// KindJSON normalises volatile fields then structurally diffs JSON bodies.
	KindJSON Kind = iota
	// KindGitRefs parses smart-HTTP ref advertisements into ref→SHA maps and
	// compares them semantically (ordering/compression ignored).
	KindGitRefs
	// KindRaw compares status + body bytes exactly.
	KindRaw
)

// Scenario is a single request replayed against both backends.
type Scenario struct {
	Name    string
	Method  string
	Path    string
	Headers map[string]string
	Body    []byte
	Kind    Kind
}

// Backend is one proxy under comparison.
type Backend struct {
	Name    string
	BaseURL string
	Client  *http.Client
}

// Captured is a backend's response to a scenario.
type Captured struct {
	Status int
	Header http.Header
	Body   []byte
}

// Divergence is one difference between the two backends for a scenario.
type Divergence struct {
	Scenario string
	Field    string // "status", a JSON path, or a ref name
	Node     string
	Go       string
}

func (d Divergence) String() string {
	return fmt.Sprintf("%s: %s — node=%q go=%q", d.Scenario, d.Field, d.Node, d.Go)
}

// Do replays a scenario against the backend and captures the response.
func (b Backend) Do(ctx context.Context, s Scenario) (Captured, error) {
	var body io.Reader
	if len(s.Body) > 0 {
		body = bytes.NewReader(s.Body)
	}
	req, err := http.NewRequestWithContext(ctx, s.Method, strings.TrimRight(b.BaseURL, "/")+s.Path, body)
	if err != nil {
		return Captured{}, err
	}
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Captured{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Captured{}, err
	}
	return Captured{Status: resp.StatusCode, Header: resp.Header, Body: raw}, nil
}

// Compare diffs two captured responses according to the scenario's Kind. Status
// is always compared; the body diff is kind-specific.
func Compare(s Scenario, node, goResp Captured) []Divergence {
	var divs []Divergence
	if node.Status != goResp.Status {
		divs = append(divs, Divergence{s.Name, "status", fmt.Sprint(node.Status), fmt.Sprint(goResp.Status)})
	}
	switch s.Kind {
	case KindJSON:
		divs = append(divs, diffJSON(s.Name, node.Body, goResp.Body)...)
	case KindGitRefs:
		divs = append(divs, diffGitRefs(s.Name, node.Body, goResp.Body)...)
	case KindRaw:
		if !bytes.Equal(node.Body, goResp.Body) {
			divs = append(divs, Divergence{s.Name, "body", string(node.Body), string(goResp.Body)})
		}
	}
	return divs
}

// Mirror replays a scenario against both backends and returns the divergences.
func Mirror(ctx context.Context, s Scenario, node, goBackend Backend) ([]Divergence, error) {
	nodeResp, err := node.Do(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("%s against node: %w", s.Name, err)
	}
	goResp, err := goBackend.Do(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("%s against go: %w", s.Name, err)
	}
	return Compare(s, nodeResp, goResp), nil
}
