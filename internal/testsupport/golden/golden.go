// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package golden defines the captured request/response fixture format used for
// contract and parity testing (tasks P0-2 / #7 and P6). Fixtures are JSON files
// recorded from the reference Node implementation; the Go backend is then
// asserted against them. See test/golden/README.md for how to capture them.
package golden

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Request is the captured client request.
type Request struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// Response is the captured server response.
type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// Fixture is a single recorded request/response pair.
type Fixture struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Request     Request  `json:"request"`
	Response    Response `json:"response"`
}

// Load reads and decodes a single fixture file. Decoding is strict
// (DisallowUnknownFields) so typos in fixture files surface immediately.
func Load(path string) (*Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var f Fixture
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Name == "" || f.Request.Method == "" || f.Request.Path == "" {
		return nil, fmt.Errorf("%s: fixture needs name, request.method and request.path", path)
	}
	return &f, nil
}

// LoadDir reads all *.json fixtures in dir (non-recursive), sorted by filename.
func LoadDir(dir string) ([]*Fixture, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)

	out := make([]*Fixture, 0, len(matches))
	for _, m := range matches {
		f, err := Load(m)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}
