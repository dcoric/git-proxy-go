// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package giturl

import (
	"net/http"
	"strings"
	"testing"
)

func TestProcessGitURL(t *testing.T) {
	tests := []struct {
		url                      string
		wantNil                  bool
		protocol, host, repoPath string
	}{
		{url: "https://github.com/finos/git-proxy.git/info/refs?service=git-upload-pack",
			protocol: "https://", host: "github.com", repoPath: "/finos/git-proxy.git"},
		{url: "https://someOtherHost.com:8080/repo.git",
			protocol: "https://", host: "someOtherHost.com:8080", repoPath: "/repo.git"},
		{url: "not-a-git-url", wantNil: true},
		{url: "https://github.com/" + strings.Repeat("a", 600) + ".git", wantNil: true},
	}
	for _, tc := range tests {
		got := ProcessGitURL(tc.url)
		if tc.wantNil {
			if got != nil {
				t.Errorf("ProcessGitURL(%q) = %+v, want nil", tc.url, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("ProcessGitURL(%q) = nil", tc.url)
		}
		if got.Protocol != tc.protocol || got.Host != tc.host || got.RepoPath != tc.repoPath {
			t.Errorf("ProcessGitURL(%q) = %+v, want {%q %q %q}", tc.url, got, tc.protocol, tc.host, tc.repoPath)
		}
	}
}

func TestProcessURLPath(t *testing.T) {
	tests := []struct {
		path              string
		wantNil           bool
		repoPath, gitPath string
	}{
		{path: "/finos/git-proxy.git/info/refs?service=git-upload-pack",
			repoPath: "/finos/git-proxy.git", gitPath: "/info/refs?service=git-upload-pack"},
		{path: "/github.com/finos/git-proxy.git/info/refs?service=git-upload-pack",
			repoPath: "/github.com/finos/git-proxy.git", gitPath: "/info/refs?service=git-upload-pack"},
		{path: "/github.com/finos/git-proxy.git", repoPath: "/github.com/finos/git-proxy.git", gitPath: "/"},
		{path: "/no-dot-git/info/refs", wantNil: true},
	}
	for _, tc := range tests {
		got := ProcessURLPath(tc.path)
		if tc.wantNil {
			if got != nil {
				t.Errorf("ProcessURLPath(%q) = %+v, want nil", tc.path, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("ProcessURLPath(%q) = nil", tc.path)
		}
		if got.RepoPath != tc.repoPath || got.GitPath != tc.gitPath {
			t.Errorf("ProcessURLPath(%q) = %+v, want {%q %q}", tc.path, got, tc.repoPath, tc.gitPath)
		}
	}
}

func TestProcessGitURLForNameAndOrg(t *testing.T) {
	tests := []struct {
		url               string
		wantNil           bool
		project, repoName string
	}{
		{url: "https://github.com/finos/git-proxy.git", project: "finos", repoName: "git-proxy.git"},
		{url: "https://someGitHost.com/repo.git", project: "", repoName: "repo.git"},
		{url: "someGitHost.com/repo.git", project: "", repoName: "repo.git"},
		{url: "https://anotherGitHost.com/project/subProject/subSubProject/repo.git",
			project: "project/subProject/subSubProject", repoName: "repo.git"},
		{url: "garbage", wantNil: true},
	}
	for _, tc := range tests {
		got := ProcessGitURLForNameAndOrg(tc.url)
		if tc.wantNil {
			if got != nil {
				t.Errorf("ProcessGitURLForNameAndOrg(%q) = %+v, want nil", tc.url, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("ProcessGitURLForNameAndOrg(%q) = nil", tc.url)
		}
		if got.Project != tc.project || got.RepoName != tc.repoName {
			t.Errorf("ProcessGitURLForNameAndOrg(%q) = %+v, want {%q %q}", tc.url, got, tc.project, tc.repoName)
		}
	}
}

func TestValidGitRequest(t *testing.T) {
	hdr := func(agent, accept string) http.Header {
		h := http.Header{}
		if agent != "" {
			h.Set("User-Agent", agent)
		}
		if accept != "" {
			h.Set("Accept", accept)
		}
		return h
	}
	tests := []struct {
		name    string
		gitPath string
		headers http.Header
		want    bool
	}{
		{"refs upload-pack ok", "/info/refs?service=git-upload-pack", hdr("git/2.40", ""), true},
		{"refs receive-pack ok", "/info/refs?service=git-receive-pack", hdr("git/2.40", ""), true},
		{"refs non-git agent", "/info/refs?service=git-upload-pack", hdr("curl/8", ""), false},
		{"refs no agent", "/info/refs?service=git-upload-pack", hdr("", ""), false},
		{"upload-pack ok", "/git-upload-pack", hdr("git/2.40", "application/x-git-upload-pack-request"), true},
		{"upload-pack no accept", "/git-upload-pack", hdr("git/2.40", ""), false},
		{"upload-pack bad accept", "/git-upload-pack", hdr("git/2.40", "text/plain"), false},
		{"unknown path", "/random", hdr("git/2.40", "application/x-git-upload-pack-request"), false},
	}
	for _, tc := range tests {
		if got := ValidGitRequest(tc.gitPath, tc.headers); got != tc.want {
			t.Errorf("%s: ValidGitRequest = %v, want %v", tc.name, got, tc.want)
		}
	}
}
