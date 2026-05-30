// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package parity

import (
	"encoding/json"
	"fmt"
	"sort"
)

// volatileKeys are response fields that legitimately differ between two
// independent runs/instances and are dropped before comparison: generated ids,
// timestamps, and audit bookkeeping. Diffing these would be all noise.
var volatileKeys = map[string]bool{
	"id":         true,
	"_id":        true,
	"timestamp":  true,
	"timepushed": true,
	"lastStep":   true,
	"dates":      true,
	"_csrf":      true,
}

// diffJSON normalises both bodies (dropping volatile keys) and structurally
// compares them, reporting one Divergence per differing leaf/missing key.
func diffJSON(scenario string, nodeBody, goBody []byte) []Divergence {
	var nodeVal, goVal any
	if err := json.Unmarshal(nodeBody, &nodeVal); err != nil {
		return []Divergence{{scenario, "<body>", "invalid JSON: " + err.Error(), trunc(goBody)}}
	}
	if err := json.Unmarshal(goBody, &goVal); err != nil {
		return []Divergence{{scenario, "<body>", trunc(nodeBody), "invalid JSON: " + err.Error()}}
	}
	return diffValues(scenario, "$", normalize(nodeVal), normalize(goVal))
}

// normalize recursively drops volatile keys from objects so structural equality
// is meaningful.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if volatileKeys[k] {
				continue
			}
			out[k] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalize(val)
		}
		return out
	default:
		return v
	}
}

// diffValues walks two normalised JSON values in parallel, recording leaf
// mismatches, type mismatches, and missing/extra object keys with their path.
func diffValues(scenario, path string, a, b any) []Divergence {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return []Divergence{{scenario, path, fmt.Sprintf("object(%d keys)", len(av)), fmt.Sprintf("%T", b)}}
		}
		return diffMaps(scenario, path, av, bv)
	case []any:
		bv, ok := b.([]any)
		if !ok {
			return []Divergence{{scenario, path, fmt.Sprintf("array(%d)", len(av)), fmt.Sprintf("%T", b)}}
		}
		if len(av) != len(bv) {
			return []Divergence{{scenario, path, fmt.Sprintf("array(%d)", len(av)), fmt.Sprintf("array(%d)", len(bv))}}
		}
		var divs []Divergence
		for i := range av {
			divs = append(divs, diffValues(scenario, fmt.Sprintf("%s[%d]", path, i), av[i], bv[i])...)
		}
		return divs
	default:
		if fmt.Sprint(a) != fmt.Sprint(b) {
			return []Divergence{{scenario, path, fmt.Sprint(a), fmt.Sprint(b)}}
		}
		return nil
	}
}

func diffMaps(scenario, path string, a, b map[string]any) []Divergence {
	var divs []Divergence
	for _, k := range sortedKeys(a, b) {
		av, aok := a[k]
		bv, bok := b[k]
		kp := path + "." + k
		switch {
		case aok && !bok:
			divs = append(divs, Divergence{scenario, kp, fmt.Sprint(av), "<missing>"})
		case !aok && bok:
			divs = append(divs, Divergence{scenario, kp, "<missing>", fmt.Sprint(bv)})
		default:
			divs = append(divs, diffValues(scenario, kp, av, bv)...)
		}
	}
	return divs
}

func sortedKeys(maps ...map[string]any) []string {
	seen := map[string]bool{}
	for _, m := range maps {
		for k := range m {
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func trunc(b []byte) string {
	const max = 120
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
