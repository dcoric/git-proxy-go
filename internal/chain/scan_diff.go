// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Block-type labels for scanDiff matches (port of BLOCK_TYPE in scanDiff.ts).
const (
	blockTypeLiteral = "Offending Literal"
	blockTypePattern = "Offending Pattern"
)

// scanDiff inspects the diff produced by getDiff for configured blocked
// literals, patterns and providers, blocking the push on a match. Port of
// src/proxy/processors/push-action/scanDiff.ts.
func (e *Engine) scanDiff(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("scanDiff")
	step.Log(fmt.Sprintf("Scanning diff: %s:%s", a.CommitFrom, a.CommitTo))

	diff := diffStepContent(a)
	step.Log(diff)

	matchers, err := e.diffMatchers(a.Project)
	if err != nil {
		step.SetError(err.Error())
		a.AddStep(step)
		return a, nil
	}

	violations := scanDiffViolations(diff, matchers, step)
	if violations != "" {
		step.Log(fmt.Sprintf("The following diff is illegal: %s:%s", a.CommitFrom, a.CommitTo))
		step.SetError("\n\n\n\nYour push has been blocked.\n\nPlease ensure your code does not contain sensitive information or URLs.\n\n\n" + violations + "\n\n")
	}

	a.AddStep(step)
	return a, nil
}

// diffStepContent returns the diff string captured by the getDiff "diff" step.
func diffStepContent(a *Action) string {
	for i := range a.Steps {
		if a.Steps[i].StepName == "diff" {
			if s, ok := a.Steps[i].Content.(string); ok {
				return s
			}
		}
	}
	return ""
}

// diffMatcher is a configured rule and its compiled (case-insensitive) regex.
type diffMatcher struct {
	typ string
	re  *regexp.Regexp
}

// diffMatchers builds the literal/pattern/provider matchers from the commit
// config. Providers are skipped when the repo's organisation is private. An
// invalid configured pattern/provider regex returns an error (the chain errors,
// as the Node `new RegExp` throw would).
func (e *Engine) diffMatchers(organization string) ([]diffMatcher, error) {
	cc := e.commitConfig()
	if cc == nil || cc.Diff == nil || cc.Diff.Block == nil {
		return nil, nil
	}
	block := cc.Diff.Block

	var matchers []diffMatcher
	for _, literal := range block.Literals {
		matchers = append(matchers, diffMatcher{blockTypeLiteral, regexp.MustCompile("(?i)" + regexp.QuoteMeta(literal))})
	}
	for _, p := range block.Patterns {
		pattern := fmt.Sprint(p)
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return nil, fmt.Errorf("compiling diff pattern %q: %w", pattern, err)
		}
		matchers = append(matchers, diffMatcher{blockTypePattern, re})
	}
	if !e.isPrivateOrganization(organization) {
		for key, value := range block.Providers {
			re, err := regexp.Compile("(?i)" + value)
			if err != nil {
				return nil, fmt.Errorf("compiling diff provider %q: %w", key, err)
			}
			matchers = append(matchers, diffMatcher{key, re})
		}
	}
	return matchers, nil
}

// isPrivateOrganization reports whether org is in the configured
// privateOrganizations list.
func (e *Engine) isPrivateOrganization(org string) bool {
	if org == "" || e.cfg == nil {
		return false
	}
	for _, p := range e.cfg.PrivateOrganizations {
		if fmt.Sprint(p) == org {
			return true
		}
	}
	return false
}

// scanMatch is one aggregated violation (a literal found in a file at lines).
type scanMatch struct {
	typ     string
	literal string
	file    string
	lines   []int
}

// scanDiffViolations parses the diff's added lines, applies the matchers, and
// returns the formatted violation report (empty when clean). Port of
// getDiffViolations + collectMatches + formatMatches.
func scanDiffViolations(diff string, matchers []diffMatcher, step *Step) string {
	if diff == "" {
		step.Log("No commit diff found, but this may be legitimate (empty diff).")
		return ""
	}

	added := parseAddedChanges(diff)
	ordered := []string{}
	byKey := map[string]*scanMatch{}
	for _, ch := range added {
		for _, m := range matchers {
			for _, found := range m.re.FindAllString(ch.content, -1) {
				key := m.typ + "_" + found + "_" + ch.file
				agg, ok := byKey[key]
				if !ok {
					agg = &scanMatch{typ: m.typ, literal: found, file: ch.file}
					byKey[key] = agg
					ordered = append(ordered, key)
				}
				agg.lines = append(agg.lines, ch.line)
			}
		}
	}
	if len(ordered) == 0 {
		return ""
	}

	step.Log("Diff is blocked via configured literals/patterns/providers.")
	var blocks []string
	for i, key := range ordered {
		m := byKey[key]
		lineStrs := make([]string, len(m.lines))
		for j, l := range m.lines {
			lineStrs[j] = strconv.Itoa(l)
		}
		blocks = append(blocks, fmt.Sprintf(
			"---------------------------------- #%d %s ------------------------------\n"+
				"    Policy Exception Type: %s\n"+
				"    DETECTED: %s \n"+
				"    FILE(S) LOCATED: %s\n"+
				"    Line(s) of code: %s",
			i+1, m.typ, m.typ, m.literal, m.file, strings.Join(lineStrs, ",")))
	}
	return strings.Join(blocks, "\n\n")
}

// addedChange is one added diff line with its new-file line number.
type addedChange struct {
	file    string
	line    int
	content string
}

var hunkHeaderRegex = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)`)

// parseAddedChanges parses a unified diff into its added lines (a minimal
// port of the parse-diff behaviour scanDiff relies on: file name, new-file line
// number, and the raw added line incl. its "+" prefix).
func parseAddedChanges(diff string) []addedChange {
	var out []addedChange
	var file string
	newLine := 0
	inHunk := false

	for _, raw := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(raw, "diff --git"):
			inHunk, file = false, ""
		case !inHunk && strings.HasPrefix(raw, "+++ "):
			file = stripDiffPath(strings.TrimPrefix(raw, "+++ "))
		case !inHunk && strings.HasPrefix(raw, "--- "):
			if file == "" {
				file = stripDiffPath(strings.TrimPrefix(raw, "--- "))
			}
		case strings.HasPrefix(raw, "@@"):
			if m := hunkHeaderRegex.FindStringSubmatch(raw); m != nil {
				newLine, _ = strconv.Atoi(m[1])
				inHunk = true
			}
		case inHunk && strings.HasPrefix(raw, "+"):
			out = append(out, addedChange{file: file, line: newLine, content: raw})
			newLine++
		case inHunk && strings.HasPrefix(raw, "-"):
			// deleted line: the new-file line number does not advance.
		case inHunk && strings.HasPrefix(raw, `\`):
			// "\ No newline at end of file": not a content line.
		case inHunk:
			// context line.
			newLine++
		}
	}
	return out
}

// stripDiffPath removes the a//b/ prefix from a diff file path and maps
// /dev/null to "".
func stripDiffPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}
