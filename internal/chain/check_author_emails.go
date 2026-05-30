// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package chain

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// emailRegex is a pragmatic e-mail validity check approximating the Node
// `validator.isEmail` used by the original processor.
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// checkAuthorEmails blocks the push when any commit author e-mail is invalid or
// fails the configured domain-allow / local-block rules. Port of
// src/proxy/processors/push-action/checkAuthorEmails.ts.
func (e *Engine) checkAuthorEmails(_ context.Context, _ *http.Request, a *Action) (*Action, error) {
	step := NewStep("checkAuthorEmails")

	// Compile the configured rules once; an invalid configured pattern aborts
	// the chain (the Node `new RegExp` would throw and be caught by executeChain).
	var domainAllow, localBlock *regexp.Regexp
	if cc := e.commitConfig(); cc != nil && cc.Author != nil && cc.Author.Email != nil {
		var err error
		if d := cc.Author.Email.Domain; d != nil && d.Allow != nil && *d.Allow != "" {
			if domainAllow, err = regexp.Compile("(?i)" + *d.Allow); err != nil {
				return a, fmt.Errorf("compiling author email domain rule: %w", err)
			}
		}
		if l := cc.Author.Email.Local; l != nil && l.Block != nil && *l.Block != "" {
			if localBlock, err = regexp.Compile("(?i)" + *l.Block); err != nil {
				return a, fmt.Errorf("compiling author email local rule: %w", err)
			}
		}
	}

	unique := uniqueStrings(authorEmails(a))
	var illegal []string
	for _, email := range unique {
		if !isEmailAllowed(email, domainAllow, localBlock) {
			illegal = append(illegal, email)
		}
	}

	if len(illegal) > 0 {
		step.Log(fmt.Sprintf("The following commit author e-mails are illegal: %s", strings.Join(illegal, ",")))
		step.SetError("Your push has been blocked. Please verify your Git configured e-mail address is valid (e.g. john.smith@example.com)")
		a.AddStep(step)
		return a, nil
	}

	step.Log(fmt.Sprintf("The following commit author e-mails are legal: %s", strings.Join(unique, ",")))
	a.AddStep(step)
	return a, nil
}

// isEmailAllowed checks an e-mail against validity and the configured rules.
func isEmailAllowed(email string, domainAllow, localBlock *regexp.Regexp) bool {
	if email == "" || !emailRegex.MatchString(email) {
		return false
	}
	local, domain, _ := strings.Cut(email, "@")
	if domainAllow != nil && !domainAllow.MatchString(domain) {
		return false
	}
	if localBlock != nil && localBlock.MatchString(local) {
		return false
	}
	return true
}

func authorEmails(a *Action) []string {
	emails := make([]string, 0, len(a.CommitData))
	for _, c := range a.CommitData {
		emails = append(emails, c.AuthorEmail)
	}
	return emails
}
