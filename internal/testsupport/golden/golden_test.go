// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package golden

import "testing"

func TestLoadDir(t *testing.T) {
	fixtures, err := LoadDir("testdata")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures loaded from testdata")
	}

	byName := make(map[string]*Fixture, len(fixtures))
	for _, f := range fixtures {
		if f.Response.Status == 0 {
			t.Errorf("%s: response.status is 0", f.Name)
		}
		byName[f.Name] = f
	}

	hc := byName["healthcheck-get"]
	if hc == nil {
		t.Fatal("missing healthcheck-get fixture")
	}
	if hc.Request.Method != "GET" || hc.Request.Path != "/api/v1/healthcheck" {
		t.Errorf("healthcheck request = %s %s, want GET /api/v1/healthcheck",
			hc.Request.Method, hc.Request.Path)
	}
	if hc.Response.Status != 200 {
		t.Errorf("healthcheck status = %d, want 200", hc.Response.Status)
	}
}

func TestLoadRejectsNonFixture(t *testing.T) {
	if _, err := Load("golden.go"); err == nil {
		t.Fatal("expected error decoding a non-fixture file, got nil")
	}
}
