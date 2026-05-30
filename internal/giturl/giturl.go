// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

// Package giturl parses the git URLs and proxy request paths that the git
// transport proxy and the chain engine operate on. It is the Go port of
// src/proxy/routes/helper.ts and is a dependency-free leaf so both
// internal/chain and internal/proxy/git can use it without an import cycle.
package giturl

import (
	"net/http"
	"regexp"
	"strings"
)

// maxURLLength rejects over-long URLs to avoid regex-based DoS (matches the
// MAX_URL_LENGTH guard in helper.ts).
const maxURLLength = 512

// Regexes ported verbatim from helper.ts.
var (
	gitURLRegex         = regexp.MustCompile(`(.+://)([^/]+)(/.+\.git)(/.+)*`)
	proxiedURLPathRegex = regexp.MustCompile(`(.+\.git)(/.*)?`)
	gitURLNameOrgRegex  = regexp.MustCompile(`(.+://)?([^/]+)/(?:(.*)/)?([^/]+\.git)`)
)

// GitURLBreakdown is the parsed form of an un-proxied git URL.
type GitURLBreakdown struct {
	Protocol string
	Host     string
	RepoPath string
}

// ProcessGitURL splits an un-proxied git URL into protocol, host and repo path,
// discarding any trailing git operation path. Returns nil on parse failure or
// over-long input. Port of processGitUrl.
func ProcessGitURL(url string) *GitURLBreakdown {
	if len(url) > maxURLLength {
		return nil
	}
	m := gitURLRegex.FindStringSubmatch(url)
	if len(m) >= 5 {
		return &GitURLBreakdown{Protocol: m[1], Host: m[2], RepoPath: m[3]}
	}
	return nil
}

// URLPathBreakdown is the parsed form of a proxy request path: the embedded
// repository path and the git operation path that follows it.
type URLPathBreakdown struct {
	RepoPath string
	GitPath  string
}

// ProcessURLPath splits a proxy request path (origin removed) into the embedded
// repository path and the git operation path. The git path defaults to "/" when
// absent. Returns nil on parse failure or over-long input. Port of
// processUrlPath.
func ProcessURLPath(requestPath string) *URLPathBreakdown {
	if len(requestPath) > maxURLLength {
		return nil
	}
	m := proxiedURLPathRegex.FindStringSubmatch(requestPath)
	if len(m) >= 3 {
		gitPath := m[2]
		if gitPath == "" {
			gitPath = "/"
		}
		return &URLPathBreakdown{RepoPath: m[1], GitPath: gitPath}
	}
	return nil
}

// GitNameBreakdown is a git URL split into project (organisation/path) and
// repository name. Project is "" when the URL has no path before the repo.
type GitNameBreakdown struct {
	Project  string
	RepoName string
}

// ProcessGitURLForNameAndOrg extracts the repository name and any preceding
// project/organisation path from a git URL embedded in a proxy request. Returns
// nil on parse failure or over-long input. Port of processGitURLForNameAndOrg.
func ProcessGitURLForNameAndOrg(gitURL string) *GitNameBreakdown {
	if len(gitURL) > maxURLLength {
		return nil
	}
	m := gitURLNameOrgRegex.FindStringSubmatch(gitURL)
	if len(m) >= 5 {
		return &GitNameBreakdown{Project: m[3], RepoName: m[4]}
	}
	return nil
}

// ValidGitRequest reports whether the request looks like a genuine git smart-HTTP
// request, given the sanitized git operation path (everything after .git) and
// the request headers. Port of validGitRequest.
func ValidGitRequest(gitPath string, headers http.Header) bool {
	agent := headers.Get("User-Agent")
	if agent == "" {
		return false
	}
	switch gitPath {
	case "/info/refs?service=git-upload-pack", "/info/refs?service=git-receive-pack":
		// The reference-discovery request carries no Accept header, so we can
		// only filter on User-Agent.
		return strings.HasPrefix(agent, "git/")
	case "/git-upload-pack", "/git-receive-pack":
		accept := headers.Get("Accept")
		if accept == "" {
			return false
		}
		return strings.HasPrefix(agent, "git/") && strings.HasPrefix(accept, "application/x-git-")
	}
	return false
}
