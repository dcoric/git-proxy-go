// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package api

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPISpecValid parses the committed OpenAPI contract and asserts its
// top-level shape and operation count, so the spec cannot silently rot or lose
// routes as the port proceeds.
func TestOpenAPISpecValid(t *testing.T) {
	raw, err := os.ReadFile("v1/openapi.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}

	var spec struct {
		OpenAPI string                    `yaml:"openapi"`
		Info    map[string]any            `yaml:"info"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse spec: %v", err)
	}

	if spec.OpenAPI == "" {
		t.Error("missing openapi version")
	}
	if spec.Info["title"] == nil {
		t.Error("missing info.title")
	}

	methods := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}
	ops := 0
	for _, item := range spec.Paths {
		for field := range item {
			if methods[field] {
				ops++
			}
		}
	}

	// 30 management operations + 3 git-transport operations.
	const wantMin = 33
	if ops < wantMin {
		t.Errorf("operation count = %d, want >= %d", ops, wantMin)
	}
	t.Logf("OpenAPI %s: %d paths, %d operations", spec.OpenAPI, len(spec.Paths), ops)
}
