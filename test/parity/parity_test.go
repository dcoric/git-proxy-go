// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package parity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiffJSONIgnoresVolatileFields(t *testing.T) {
	node := []byte(`{"id":"abc","timestamp":111,"repo":"r","allowPush":false}`)
	goJSON := []byte(`{"id":"xyz","timestamp":999,"repo":"r","allowPush":false}`)
	if divs := diffJSON("s", node, goJSON); len(divs) != 0 {
		t.Errorf("volatile-only differences reported as divergences: %v", divs)
	}
}

func TestDiffJSONCatchesValueDifference(t *testing.T) {
	node := []byte(`{"repo":"r","allowPush":false}`)
	goJSON := []byte(`{"repo":"r","allowPush":true}`)
	divs := diffJSON("s", node, goJSON)
	if len(divs) != 1 || divs[0].Field != "$.allowPush" {
		t.Fatalf("expected one divergence at $.allowPush, got %v", divs)
	}
	if divs[0].Node != "false" || divs[0].Go != "true" {
		t.Errorf("divergence values = node %q go %q", divs[0].Node, divs[0].Go)
	}
}

func TestDiffJSONReportsMissingKey(t *testing.T) {
	node := []byte(`{"a":1,"b":2}`)
	goJSON := []byte(`{"a":1}`)
	divs := diffJSON("s", node, goJSON)
	if len(divs) != 1 || divs[0].Field != "$.b" || divs[0].Go != "<missing>" {
		t.Fatalf("expected $.b missing on go side, got %v", divs)
	}
}

func TestDiffJSONNestedAndArrays(t *testing.T) {
	node := []byte(`{"cfg":{"x":[1,2,3]}}`)
	goJSON := []byte(`{"cfg":{"x":[1,2,4]}}`)
	divs := diffJSON("s", node, goJSON)
	if len(divs) != 1 || divs[0].Field != "$.cfg.x[2]" {
		t.Fatalf("expected divergence at $.cfg.x[2], got %v", divs)
	}
}

func TestDiffGitRefsSemanticEquality(t *testing.T) {
	// Same refs, different order, and capabilities only on the node side's first
	// ref — semantically identical.
	node := refAdv(
		"00000000000000000000000000000000000000ab refs/heads/main\x00report-status",
		"00000000000000000000000000000000000000cd refs/heads/dev",
	)
	goAdv := refAdv(
		"00000000000000000000000000000000000000cd refs/heads/dev",
		"00000000000000000000000000000000000000ab refs/heads/main",
	)
	if divs := diffGitRefs("s", node, goAdv); len(divs) != 0 {
		t.Errorf("semantically equal advertisements reported divergences: %v", divs)
	}
}

func TestDiffGitRefsCatchesShaAndMissing(t *testing.T) {
	node := refAdv(
		"00000000000000000000000000000000000000ab refs/heads/main",
		"00000000000000000000000000000000000000cd refs/heads/dev",
	)
	goAdv := refAdv(
		"00000000000000000000000000000000000000ff refs/heads/main", // different SHA
	)
	divs := diffGitRefs("s", node, goAdv)
	if len(divs) != 2 {
		t.Fatalf("expected 2 divergences (sha mismatch + missing dev), got %v", divs)
	}
	byRef := map[string]Divergence{}
	for _, d := range divs {
		byRef[d.Field] = d
	}
	if d := byRef["refs/heads/main"]; d.Node != "00000000000000000000000000000000000000ab" || d.Go != "00000000000000000000000000000000000000ff" {
		t.Errorf("main SHA divergence wrong: %+v", d)
	}
	if d := byRef["refs/heads/dev"]; d.Go != "<missing>" {
		t.Errorf("expected dev missing on go side, got %+v", d)
	}
}

func TestMirrorComparesBothBackends(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"node","shared":1}`))
	}))
	defer node.Close()
	goSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"mode":"go","shared":1}`))
	}))
	defer goSrv.Close()

	divs, err := Mirror(context.Background(),
		Scenario{Name: "home", Method: "GET", Path: "/api", Kind: KindJSON},
		Backend{Name: "node", BaseURL: node.URL},
		Backend{Name: "go", BaseURL: goSrv.URL},
	)
	if err != nil {
		t.Fatalf("Mirror: %v", err)
	}
	// Expect a status divergence (200 vs 418) and a $.mode value divergence.
	fields := map[string]bool{}
	for _, d := range divs {
		fields[d.Field] = true
	}
	if !fields["status"] || !fields["$.mode"] {
		t.Errorf("expected status + $.mode divergences, got %v", divs)
	}
}

// refAdv builds a smart-HTTP upload-pack ref advertisement: the service header,
// a flush, the ref lines, and a trailing flush.
func refAdv(refLines ...string) []byte {
	out := pkt("# service=git-upload-pack\n")
	out = append(out, []byte("0000")...)
	for _, l := range refLines {
		out = append(out, pkt(l+"\n")...)
	}
	out = append(out, []byte("0000")...)
	return out
}

func pkt(payload string) []byte {
	const hex = "0123456789abcdef"
	n := len(payload) + 4
	return append([]byte{hex[(n>>12)&0xf], hex[(n>>8)&0xf], hex[(n>>4)&0xf], hex[n&0xf]}, payload...)
}
