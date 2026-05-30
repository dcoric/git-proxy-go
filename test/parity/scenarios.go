// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package parity

// DefaultAPIScenarios are read-only, unauthenticated management-API requests
// both proxies should answer identically. Point the backends at each proxy's
// management server (the UI port).
func DefaultAPIScenarios() []Scenario {
	return []Scenario{
		{Name: "api-home", Method: "GET", Path: "/api", Kind: KindJSON},
		{Name: "auth-config", Method: "GET", Path: "/api/auth/config", Kind: KindJSON},
		{Name: "config-attestation", Method: "GET", Path: "/api/v1/config/attestation", Kind: KindJSON},
		{Name: "config-contactEmail", Method: "GET", Path: "/api/v1/config/contactEmail", Kind: KindJSON},
		{Name: "config-urlShortener", Method: "GET", Path: "/api/v1/config/urlShortener", Kind: KindJSON},
		{Name: "config-uiRouteAuth", Method: "GET", Path: "/api/v1/config/uiRouteAuth", Kind: KindJSON},
	}
}

// DefaultGitScenarios are read-only git smart-HTTP requests for a repo the
// proxies are configured to serve (e.g. "github.com/org/repo.git"). Point the
// backends at each proxy's git-transport server.
func DefaultGitScenarios(repo string) []Scenario {
	return []Scenario{
		{
			Name:   "info-refs-upload-pack",
			Method: "GET",
			Path:   "/" + repo + "/info/refs?service=git-upload-pack",
			Kind:   KindGitRefs,
		},
	}
}
