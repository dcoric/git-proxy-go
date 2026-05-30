// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// SSH agent-forwarding channel/request types (OpenSSH protocol).
const (
	agentRequestType = "auth-agent-req@openssh.com"
	agentChannelType = "auth-agent@openssh.com"
)

// openForwardedAgent opens an agent-forwarding channel back to the client and
// returns a client for the agent the client forwarded. The proxy uses this
// agent to authenticate to the upstream git server *as the client* — the
// client's private keys never reach the proxy (P5-4, ported from
// AgentForwarding/AgentProxy in finos/git-proxy PR #1332; the x/crypto/ssh
// channel API replaces the ssh2 internals the Node version reached into).
//
// The caller must Close the returned io.Closer (the channel) when done.
func openForwardedAgent(conn *ssh.ServerConn) (agent.ExtendedAgent, io.Closer, error) {
	ch, reqs, err := conn.OpenChannel(agentChannelType, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("opening agent-forwarding channel: %w", err)
	}
	go ssh.DiscardRequests(reqs)
	return agent.NewClient(ch), ch, nil
}
