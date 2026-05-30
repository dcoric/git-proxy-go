// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package parity

import (
	"sort"
	"strconv"
	"strings"
)

// diffGitRefs compares two smart-HTTP ref advertisements semantically: it parses
// each into a ref→SHA map and reports refs that differ, are missing, or are
// extra. Byte equality is deliberately avoided — pkt-line framing, capability
// ordering, and pack compression legitimately differ between implementations
// (P6-3), so only the resolved refs/SHAs matter.
func diffGitRefs(scenario string, nodeBody, goBody []byte) []Divergence {
	nodeRefs := parseRefs(nodeBody)
	goRefs := parseRefs(goBody)

	var divs []Divergence
	for _, ref := range sortedRefNames(nodeRefs, goRefs) {
		ns, nok := nodeRefs[ref]
		gs, gok := goRefs[ref]
		switch {
		case nok && !gok:
			divs = append(divs, Divergence{scenario, ref, ns, "<missing>"})
		case !nok && gok:
			divs = append(divs, Divergence{scenario, ref, "<missing>", gs})
		case ns != gs:
			divs = append(divs, Divergence{scenario, ref, ns, gs})
		}
	}
	return divs
}

// parseRefs walks a smart-HTTP pkt-line ref advertisement into a ref→SHA map. It
// tolerates the optional "# service=…" header line, flush packets, and the
// capability list on the first ref (after a NUL). Malformed lines are skipped
// rather than panicking — the input is an untrusted upstream response.
func parseRefs(body []byte) map[string]string {
	refs := map[string]string{}
	for off := 0; off+4 <= len(body); {
		length, err := strconv.ParseUint(string(body[off:off+4]), 16, 32)
		if err != nil {
			break // not a pkt-line stream from here on
		}
		if length == 0 { // flush
			off += 4
			continue
		}
		if int(length) < 4 || off+int(length) > len(body) {
			break // truncated/invalid
		}
		payload := strings.TrimRight(string(body[off+4:off+int(length)]), "\n")
		off += int(length)

		if strings.HasPrefix(payload, "#") { // service header
			continue
		}
		// "<sha> <refname>[\x00capabilities]"
		sha, rest, ok := strings.Cut(payload, " ")
		if !ok {
			continue
		}
		ref, _, _ := strings.Cut(rest, "\x00") // drop capabilities on the first ref
		ref = strings.TrimSpace(ref)
		if ref != "" {
			refs[ref] = sha
		}
	}
	return refs
}

func sortedRefNames(maps ...map[string]string) []string {
	seen := map[string]bool{}
	for _, m := range maps {
		for k := range m {
			seen[k] = true
		}
	}
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
