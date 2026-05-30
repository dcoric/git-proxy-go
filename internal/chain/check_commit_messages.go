// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// checkCommitMessages blocks the push when any commit message matches a
// configured blocked literal or pattern. Port of
// src/proxy/processors/push-action/checkCommitMessages.ts.
func (e *Engine) checkCommitMessages(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("checkCommitMessages")

	unique := uniqueStrings(commitMessages(a))
	var illegal []string
	for _, msg := range unique {
		if !e.isMessageAllowed(msg, step) {
			illegal = append(illegal, msg)
		}
	}

	if len(illegal) > 0 {
		encoded, _ := json.Marshal(illegal)
		step.Log(fmt.Sprintf("The following commit messages are illegal: %s", strings.Join(illegal, ",")))
		step.SetError(fmt.Sprintf("\n\n\nYour push has been blocked.\nPlease ensure your commit message(s) does not contain sensitive information or URLs.\n\nThe following commit messages are illegal: %s\n\n", encoded))
		a.AddStep(step)
		return a, nil
	}

	step.Log(fmt.Sprintf("The following commit messages are legal: %s", strings.Join(unique, ",")))
	a.AddStep(step)
	return a, nil
}

// isMessageAllowed reports whether a commit message passes the configured block
// rules. A blank message, or a literal/pattern match, blocks it; an invalid
// configured pattern also blocks (mirroring the Node try/catch returning false).
func (e *Engine) isMessageAllowed(message string, step *Step) bool {
	cc := e.commitConfig()
	if message == "" {
		step.Log("No commit message included.")
		return false
	}
	if cc == nil || cc.Message == nil || cc.Message.Block == nil {
		return true
	}
	block := cc.Message.Block

	lower := strings.ToLower(message)
	for _, literal := range block.Literals {
		if strings.Contains(lower, strings.ToLower(literal)) {
			step.Log("Commit message is blocked via configured literals/patterns.")
			return false
		}
	}
	for _, pattern := range block.Patterns {
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			step.SetError(fmt.Sprintf("Error checking commit messages: %s", err))
			return false
		}
		if re.MatchString(message) {
			step.Log("Commit message is blocked via configured literals/patterns.")
			return false
		}
	}
	return true
}

func commitMessages(a *Action) []string {
	msgs := make([]string, 0, len(a.CommitData))
	for _, c := range a.CommitData {
		msgs = append(msgs, c.Message)
	}
	return msgs
}

// uniqueStrings returns the distinct values of s, preserving first-seen order.
func uniqueStrings(s []string) []string {
	seen := make(map[string]bool, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
