// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GitProxy Contributors

package ssh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/dcoric/git-proxy-go/internal/chain"
)

// maxPackBytes caps a buffered push pack (matches the HTTP proxy's body limit).
const maxPackBytes = 1 << 30 // 1 GiB

// userAgentSSH labels chain requests synthesised from SSH (port of the
// 'git/ssh-proxy' user-agent in createChainRequest).
const userAgentSSH = "git/ssh-proxy"

// agentForwardingRequiredMessage is shown to clients that connect without
// forwarding their SSH agent (the proxy needs it to authenticate upstream).
const agentForwardingRequiredMessage = "SSH agent forwarding is required.\n\n" +
	"The proxy authenticates to the upstream git server using your SSH keys via\n" +
	"agent forwarding; your private keys never reach the proxy. Enable it with\n" +
	"'ForwardAgent yes' (or 'ssh -A' / a per-host config) and ensure ssh-agent is\n" +
	"running with your key loaded (ssh-add -l)."

// chainExecutor runs a synthesised request through the security chain
// (satisfied by *chain.Engine).
type chainExecutor interface {
	Execute(ctx context.Context, r *http.Request) *chain.Action
}

// ProxyHandler is the real GitHandler (P5-2): it routes git-over-SSH commands
// through the same security chain as the HTTP proxy and forwards approved
// traffic to the upstream git server over SSH, authenticating as the client via
// their forwarded agent.
//
// Pulls (clone/fetch) run the pull chain (authorisation) then proxy the fetch.
// Pushes relay the upstream ref advertisement, buffer the client's pack, run the
// push chain, and forward the pack only if the chain approves it.
//
// NOTE: the push chain's pullRemote step currently clones over HTTPS with basic
// auth, which an SSH-originated request lacks, so SSH pushes are intercepted and
// evaluated but rejected at pullRemote until an SSH clone variant (PullRemoteSSH)
// lands. Live byte-level proxying is validated by the P5-6 interop gate.
type ProxyHandler struct {
	engine   chainExecutor
	upstream Upstream
}

// NewProxyHandler builds the SSH git handler from the chain engine and an
// upstream dialer.
func NewProxyHandler(engine chainExecutor, upstream Upstream) *ProxyHandler {
	return &ProxyHandler{engine: engine, upstream: upstream}
}

// Handle routes a parsed git command through the chain and (if approved) the
// upstream. It always sends the client a human-readable reason on rejection.
func (h *ProxyHandler) Handle(ctx context.Context, req GitRequest) (uint32, error) {
	if !req.AgentForwarding || req.OpenAgent == nil {
		writeClientError(req.Channel, agentForwardingRequiredMessage)
		return 1, nil
	}
	ag, closer, err := req.OpenAgent()
	if err != nil {
		writeClientError(req.Channel, "failed to access forwarded SSH agent: "+err.Error())
		return 1, err
	}
	defer func() { _ = closer.Close() }()

	host, repoPath := splitHostRepo(req.RepoPath)
	remoteCommand := fmt.Sprintf("git-%s '%s'", req.Op, repoPath)

	switch req.Op {
	case "receive-pack":
		return h.handlePush(ctx, req, host, remoteCommand, ag)
	case "upload-pack":
		return h.handlePull(ctx, req, host, remoteCommand, ag)
	default:
		writeClientError(req.Channel, "unsupported operation: "+req.Op)
		return 1, nil
	}
}

// handlePull authorises a fetch/clone via the pull chain, then proxies the
// upload-pack stream bidirectionally between client and upstream.
func (h *ProxyHandler) handlePull(ctx context.Context, req GitRequest, host, remoteCommand string, ag agent.ExtendedAgent) (uint32, error) {
	action := h.engine.Execute(ctx, chainRequest(ctx, req.Op, req.RepoPath, nil))
	if action.Error || action.Blocked {
		writeClientError(req.Channel, blockMessage(action))
		return 1, nil
	}

	sess, err := h.upstream.Run(ctx, host, remoteCommand, ag)
	if err != nil {
		writeClientError(req.Channel, "upstream connection failed: "+err.Error())
		return 1, err
	}
	defer func() { _ = sess.Close() }()

	go func() {
		_, _ = io.Copy(sess.Stdin, req.Channel)
		_ = sess.Stdin.Close()
	}()
	go func() { _, _ = io.Copy(req.Channel.Stderr(), sess.Stderr) }()
	_, _ = io.Copy(req.Channel, sess.Stdout)
	return sess.Wait()
}

// handlePush relays the upstream capabilities, buffers and validates the client
// pack through the push chain, then forwards it upstream only if approved. Port
// of handlePushOperation + fetchGitHubCapabilities + forwardPackDataToRemote.
func (h *ProxyHandler) handlePush(ctx context.Context, req GitRequest, host, remoteCommand string, ag agent.ExtendedAgent) (uint32, error) {
	sess, err := h.upstream.Run(ctx, host, remoteCommand, ag)
	if err != nil {
		writeClientError(req.Channel, "upstream connection failed: "+err.Error())
		return 1, err
	}
	defer func() { _ = sess.Close() }()

	// Relay the upstream ref advertisement so the client will send its pack.
	caps, err := readRefAdvertisement(sess.Stdout)
	if err != nil {
		writeClientError(req.Channel, "failed to read upstream capabilities: "+err.Error())
		return 1, err
	}
	if _, err := req.Channel.Write(caps); err != nil {
		return 1, err
	}

	// Buffer the client's commands + pack so the chain can inspect them before
	// anything reaches the upstream (the security boundary).
	pack, err := io.ReadAll(io.LimitReader(req.Channel, maxPackBytes))
	if err != nil {
		writeClientError(req.Channel, "failed to read pack data: "+err.Error())
		return 1, err
	}
	if len(pack) == 0 {
		return 0, nil // client disconnected without pushing
	}

	action := h.engine.Execute(ctx, chainRequest(ctx, req.Op, req.RepoPath, pack))
	if action.Error || action.Blocked {
		writeClientError(req.Channel, blockMessage(action))
		return 1, nil
	}

	// Approved: forward the pack upstream and relay the report-status back.
	if _, err := sess.Stdin.Write(pack); err != nil {
		return 1, err
	}
	_ = sess.Stdin.Close()
	go func() { _, _ = io.Copy(req.Channel.Stderr(), sess.Stderr) }()
	_, _ = io.Copy(req.Channel, sess.Stdout)
	return sess.Wait()
}

// chainRequest synthesises the *http.Request the chain expects from an SSH git
// command, mirroring the HTTP proxy path so parseAction resolves the same repo
// URL. Port of createChainRequest.
func chainRequest(ctx context.Context, op, fullRepoPath string, pack []byte) *http.Request {
	method := http.MethodGet
	contentType := "application/x-git-upload-pack-request"
	if op == "receive-pack" {
		method = http.MethodPost
		contentType = "application/x-git-receive-pack-request"
	}
	target := "/" + fullRepoPath + "/git-" + op

	r, _ := http.NewRequestWithContext(ctx, method, target, nil)
	r.RequestURI = target // parseAction reads RequestURI (server-request field)
	r.Header.Set("User-Agent", userAgentSSH)
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-Forwarded-Proto", "https")
	if pack != nil {
		r = r.WithContext(chain.WithRawBody(r.Context(), pack))
	}
	return r
}

// splitHostRepo splits a host-qualified repo path ("github.com/org/repo.git")
// into the upstream host and the leading-slash repo path the remote git command
// expects ("/org/repo.git").
func splitHostRepo(full string) (host, repoPath string) {
	if i := strings.IndexByte(full, '/'); i >= 0 {
		return full[:i], full[i:]
	}
	return full, "/"
}

// readRefAdvertisement reads pkt-lines from r up to and including the first
// flush packet (the git ref advertisement), returning the raw bytes to relay.
func readRefAdvertisement(r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return nil, fmt.Errorf("reading pkt-line header: %w", err)
		}
		buf.Write(header)
		length, err := strconv.ParseUint(string(header), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid pkt-line length %q: %w", header, err)
		}
		if length == 0 {
			return buf.Bytes(), nil // flush packet ends the advertisement
		}
		if length < 4 {
			return nil, fmt.Errorf("invalid pkt-line length %d", length)
		}
		payload := make([]byte, length-4)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("reading pkt-line payload: %w", err)
		}
		buf.Write(payload)
	}
}

// blockMessage derives the client-facing rejection reason from the action.
func blockMessage(a *chain.Action) string {
	if a.BlockedMessage != nil && *a.BlockedMessage != "" {
		return *a.BlockedMessage
	}
	if a.ErrorMessage != nil && *a.ErrorMessage != "" {
		return *a.ErrorMessage
	}
	return "request blocked by git-proxy"
}

// writeClientError writes a message to the client's stderr stream.
func writeClientError(ch ssh.Channel, msg string) {
	_, _ = ch.Stderr().Write([]byte("git-proxy: " + strings.TrimRight(msg, "\n") + "\n"))
}
