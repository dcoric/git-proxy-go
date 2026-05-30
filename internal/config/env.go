// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package config

import "os"

// ServerEnv holds the listener ports and UI host, sourced from environment
// variables. It mirrors serverConfig in src/config/env.ts; the values are not
// part of proxy.config.json. The git-transport ports (P4) are carried here too
// so the entrypoint has a single source for them.
type ServerEnv struct {
	// UIPort / HTTPSUIPort are the management UI listeners (this is the server
	// built in P1). HTTPSUIPort is only used when tls.enabled is set.
	UIPort      string
	HTTPSUIPort string
	// GitServerPort / HTTPSGitServerPort are the git-transport listeners (P4).
	GitServerPort      string
	HTTPSGitServerPort string
	// UIHost is the externally reachable UI host, used to build redirect URLs.
	UIHost string

	// SSHEnabled turns on the git-over-SSH server (P5). SSHPort is its listener;
	// SSHHostKeyPath is where the proxy's SSH host key is loaded/generated.
	SSHEnabled     bool
	SSHPort        string
	SSHHostKeyPath string
}

// LoadServerEnv reads the listener configuration from the environment, applying
// the same defaults as the Node build.
func LoadServerEnv() ServerEnv {
	return ServerEnv{
		UIPort:             envOr("GIT_PROXY_UI_PORT", "8080"),
		HTTPSUIPort:        envOr("GIT_PROXY_HTTPS_UI_PORT", "8444"),
		GitServerPort:      envOr("GIT_PROXY_SERVER_PORT", "8000"),
		HTTPSGitServerPort: envOr("GIT_PROXY_HTTPS_SERVER_PORT", "8443"),
		UIHost:             envOr("GIT_PROXY_UI_HOST", "http://localhost"),
		SSHEnabled:         os.Getenv("GIT_PROXY_SSH_ENABLED") == "true",
		SSHPort:            envOr("GIT_PROXY_SSH_PORT", "2222"),
		SSHHostKeyPath:     envOr("GIT_PROXY_SSH_HOST_KEY_PATH", ".ssh/proxy_host_key"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
