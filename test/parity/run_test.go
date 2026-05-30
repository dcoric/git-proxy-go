// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package parity

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestParityAgainstBackends drives the harness against running Node + Go proxies
// in staging. It is skipped unless the backend URLs are set, so CI (which has no
// Node proxy) ignores it while the unit tests below still run.
//
//	PARITY_NODE_API_URL / PARITY_GO_API_URL   management servers (UI port)
//	PARITY_NODE_GIT_URL / PARITY_GO_GIT_URL   git-transport servers
//	PARITY_GIT_REPO                           host-qualified repo, e.g. github.com/org/repo.git
func TestParityAgainstBackends(t *testing.T) {
	apiNode, apiGo := os.Getenv("PARITY_NODE_API_URL"), os.Getenv("PARITY_GO_API_URL")
	gitNode, gitGo := os.Getenv("PARITY_NODE_GIT_URL"), os.Getenv("PARITY_GO_GIT_URL")
	repo := os.Getenv("PARITY_GIT_REPO")

	if apiNode == "" && gitNode == "" {
		t.Skip("set PARITY_*_URL to run parity against live backends (see package doc)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	run := func(t *testing.T, scenarios []Scenario, node, goBackend Backend) {
		for _, s := range scenarios {
			divs, err := Mirror(ctx, s, node, goBackend)
			if err != nil {
				t.Errorf("%s: mirror failed: %v", s.Name, err)
				continue
			}
			for _, d := range divs {
				t.Errorf("divergence: %s", d)
			}
		}
	}

	if apiNode != "" && apiGo != "" {
		run(t, DefaultAPIScenarios(), Backend{Name: "node", BaseURL: apiNode}, Backend{Name: "go", BaseURL: apiGo})
	}
	if gitNode != "" && gitGo != "" && repo != "" {
		run(t, DefaultGitScenarios(repo), Backend{Name: "node", BaseURL: gitNode}, Backend{Name: "go", BaseURL: gitGo})
	}
}
